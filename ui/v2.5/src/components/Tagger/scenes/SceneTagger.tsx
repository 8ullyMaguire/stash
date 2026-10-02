import React, { useCallback, useContext, useMemo, useState } from "react";
import * as GQL from "src/core/generated-graphql";
import { SceneQueue } from "src/models/sceneQueue";
import { Button, Form } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";

import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import { OperationButton } from "src/components/Shared/OperationButton";
import { ISceneQueryResult, TaggerStateContext } from "../context";
import Config from "./Config";
import { TaggerScene } from "./TaggerScene";
import { SceneTaggerModals } from "./sceneTaggerModals";
import { SceneSearchResults } from "./StashSearchResult";
import { useConfigurationContext } from "src/hooks/Config";
import { useLightbox } from "src/hooks/Lightbox/hooks";
import { ConfigButton } from "../TaggerConfig";
import { BulkCreate, BulkAction } from "./BulkCreate";
import { mutateScenesOrganized } from "src/core/StashService";

const Scene: React.FC<{
  scene: GQL.SlimSceneDataFragment;
  searchResult?: ISceneQueryResult;
  queue?: SceneQueue;
  index: number;
  showLightboxImage: (imagePath: string) => void;
  selected?: boolean;
  onSelectedChanged?: (selected: boolean, shiftKey: boolean) => void;
}> = ({
  scene,
  searchResult,
  queue,
  index,
  showLightboxImage,
  selected,
  onSelectedChanged,
}) => {
  const intl = useIntl();
  const { currentSource, doSceneQuery, doSceneFragmentScrape, loading } =
    useContext(TaggerStateContext);
  const { configuration } = useConfigurationContext();

  const cont = configuration?.interface.continuePlaylistDefault ?? false;

  const sceneLink = useMemo(
    () =>
      queue
        ? queue.makeLink(scene.id, { sceneIndex: index, continue: cont })
        : `/scenes/${scene.id}`,
    [queue, scene.id, index, cont]
  );

  const errorMessage = useMemo(() => {
    if (searchResult?.error) {
      return searchResult.error;
    } else if (searchResult && searchResult.results?.length === 0) {
      return intl.formatMessage({
        id: "component_tagger.results.match_failed_no_result",
      });
    }
  }, [intl, searchResult]);

  return (
    <TaggerScene
      loading={loading}
      scene={scene}
      url={sceneLink}
      errorMessage={errorMessage}
      doSceneQuery={
        currentSource?.supportSceneQuery
          ? async (v) => {
              await doSceneQuery(scene.id, v);
            }
          : undefined
      }
      scrapeSceneFragment={
        currentSource?.supportSceneFragment
          ? async () => {
              await doSceneFragmentScrape(scene.id);
            }
          : undefined
      }
      showLightboxImage={showLightboxImage}
      queue={queue}
      index={index}
      selected={selected}
      onSelectedChanged={onSelectedChanged}
    >
      {searchResult?.results?.length ? (
        <SceneSearchResults scenes={searchResult.results} target={scene} />
      ) : undefined}
    </TaggerScene>
  );
};

interface ITaggerProps {
  scenes: GQL.SlimSceneDataFragment[];
  queue?: SceneQueue;
  selectedIds: Set<string>;
  onSelectChange: (id: string, selected: boolean, shiftKey: boolean) => void;
}

export const Tagger: React.FC<ITaggerProps> = ({
  scenes,
  queue,
  selectedIds,
  onSelectChange,
}) => {
  const {
    sources,
    setCurrentSource,
    currentSource,
    doMultiSceneFragmentScrape,
    stopMultiScrape,
    searchResults,
    loading,
    loadingMulti,
    multiError,
    submitFingerprints,
    pendingFingerprints,
  } = useContext(TaggerStateContext);
  const [showConfig, setShowConfig] = useState(false);
  const [hideUnmatched, setHideUnmatched] = useState(false);

  const intl = useIntl();

  const hasSelection = selectedIds.size > 0;

  function handleSourceSelect(e: React.ChangeEvent<HTMLSelectElement>) {
    setCurrentSource(sources!.find((s) => s.id === e.currentTarget.value));
  }

  function renderSourceSelector() {
    return (
      <Form.Group controlId="scraper">
        <Form.Label>
          <FormattedMessage id="component_tagger.config.source" />
        </Form.Label>
        <div>
          <Form.Control
            as="select"
            value={currentSource?.id}
            className="input-control"
            disabled={loading || !sources.length}
            onChange={handleSourceSelect}
          >
            {!sources.length && <option>No scraper sources</option>}
            {sources.map((i) => (
              <option value={i.id} key={i.id}>
                {i.displayName}
              </option>
            ))}
          </Form.Control>
        </div>
      </Form.Group>
    );
  }

  const [spriteImage, setSpriteImage] = useState<string | null>(null);
  const lightboxImage = useMemo(
    () => [{ paths: { thumbnail: spriteImage, image: spriteImage } }],
    [spriteImage]
  );
  const showLightbox = useLightbox({
    images: lightboxImage,
  });
  function showLightboxImage(imagePath: string) {
    setSpriteImage(imagePath);
    showLightbox({ images: lightboxImage });
  }

  const filteredScenes = useMemo(
    () =>
      !hideUnmatched
        ? scenes
        : scenes.filter((s) => searchResults[s.id]?.results?.length),
    [scenes, searchResults, hideUnmatched]
  );

  const toggleHideUnmatchedScenes = () => {
    setHideUnmatched(!hideUnmatched);
  };

  // #3122: project the fragment fields the three actions decide on. Only the fields the
  // selection reads are pulled across, so the projection cannot drift into a second,
  // subtly-different definition of "missing" living in the component.
  const bulkScenes = useMemo(
    () =>
      filteredScenes.map((s) => ({
        id: s.id,
        organized: s.organized,
        studio: s.studio?.name,
        date: s.date,
        performerNames: (s.performers ?? []).map((p) => p.name ?? ""),
        tagNames: (s.tags ?? []).map((t) => t.name ?? ""),
      })),
    [filteredScenes]
  );

  /**
   * Apply a bulk action to a chunk of scenes.
   *
   * #3122's three actions as the plan defines them: All/New/Missing select SCENES, and the
   * mutation is organised-marking -- "New = candidates with no organized flag", so creating
   * one means marking it. Metadata-filling is left to the per-scene tagger, which is the
   * surface that exists for it and which the user is already on; having a bulk button guess
   * at metadata would write the wrong thing silently.
   */
  const handleBulkCreate = useCallback(
    async (_action: BulkAction, sceneIds: string[]) => {
      // `mutateScenesOrganized` takes the whole chunk, so one call per chunk rather than one
      // per scene.
      await mutateScenesOrganized(sceneIds);
    },
    []
  );

  function maybeRenderShowHideUnmatchedButton() {
    if (Object.keys(searchResults).length) {
      return (
        <Button onClick={toggleHideUnmatchedScenes}>
          <FormattedMessage
            id="component_tagger.verb_toggle_unmatched"
            values={{
              toggle: (
                <FormattedMessage
                  id={`actions.${!hideUnmatched ? "hide" : "show"}`}
                />
              ),
            }}
          />
        </Button>
      );
    }
  }

  function maybeRenderSubmitFingerprintsButton() {
    if (pendingFingerprints.length) {
      return (
        <OperationButton
          className="ml-1"
          operation={submitFingerprints}
          disabled={loading || loadingMulti}
        >
          <span>
            <FormattedMessage
              id="component_tagger.verb_submit_fp"
              values={{ fpCount: pendingFingerprints.length }}
            />
          </span>
        </OperationButton>
      );
    }
  }

  function renderFragmentScrapeButton() {
    if (!currentSource?.supportSceneFragment) {
      return;
    }

    // Use selected scenes if any, otherwise all scenes
    const scenesToScrape = hasSelection
      ? scenes.filter((s) => selectedIds.has(s.id))
      : scenes;

    if (scenesToScrape.length === 0) {
      return;
    }

    if (loadingMulti) {
      return (
        <Button
          className="ml-1"
          variant="danger"
          onClick={() => {
            stopMultiScrape();
          }}
        >
          <LoadingIndicator message="" inline small />
          <span className="ml-2">
            {intl.formatMessage({ id: "actions.stop" })}
          </span>
        </Button>
      );
    }

    // Change button text based on selection state
    const buttonTextId = hasSelection
      ? "component_tagger.verb_scrape_selected"
      : "component_tagger.verb_scrape_all";

    return (
      <div className="ml-1">
        <OperationButton
          disabled={loading}
          operation={async () => {
            await doMultiSceneFragmentScrape(scenesToScrape.map((s) => s.id));
          }}
        >
          {intl.formatMessage({ id: buttonTextId })}
        </OperationButton>
        {multiError && (
          <>
            <br />
            <b className="text-danger">{multiError}</b>
          </>
        )}
      </div>
    );
  }

  return (
    <SceneTaggerModals>
      <div className="tagger-container mx-md-auto">
        <div className="tagger-container-header">
          <div className="d-flex justify-content-between align-items-center flex-wrap">
            <div className="w-auto">{renderSourceSelector()}</div>
            <div className="d-flex">
              <BulkCreate scenes={bulkScenes} onCreate={handleBulkCreate} />
              {maybeRenderShowHideUnmatchedButton()}
              {maybeRenderSubmitFingerprintsButton()}
              {renderFragmentScrapeButton()}
              <div className="ml-2">
                <ConfigButton
                  showConfig={showConfig}
                  onClick={() => setShowConfig(!showConfig)}
                />
              </div>
            </div>
          </div>
          <Config show={showConfig} />
        </div>
        <div>
          {filteredScenes.map((s, i) => (
            <Scene
              key={s.id}
              scene={s}
              searchResult={searchResults[s.id]}
              index={i}
              showLightboxImage={showLightboxImage}
              queue={queue}
              selected={selectedIds.has(s.id)}
              onSelectedChanged={(selected, shiftKey) =>
                onSelectChange(s.id, selected, shiftKey)
              }
            />
          ))}
        </div>
      </div>
    </SceneTaggerModals>
  );
};
