import { useCallback, useEffect, useState } from "react";

const STORAGE_KEY = "caption-offset";

function readAll(): Record<string, number> {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return {};
    return parsed as Record<string, number>;
  } catch (e) {
    return {};
  }
}

/**
 * Read the stored offset for a language without subscribing to changes.
 *
 * ScenePlayer builds every track URL inside a callback rather than during render, so it needs the value
 * at call time and must not subscribe -- a change to one language's offset should not force a re-render
 * of the whole player. Re-render happens anyway because the controls that call setOffset live in the
 * player's own state.
 */
export function readCaptionOffset(languageCode: string | undefined): number {
  if (!languageCode) {
    return 0;
  }

  const stored = readAll()[languageCode];
  return typeof stored === "number" && Number.isFinite(stored) ? stored : 0;
}

/**
 * Per-language caption offset, in milliseconds.
 *
 * stash#4771. Offsets are stored per language rather than globally because the reason a track needs
 * shifting is almost always a property of the track -- a fan-subtitled release timed to a different
 * cut, or a broadcast capture with a known delay -- and that correction does not transfer to the next
 * scene's English track.
 *
 * localStorage rather than server state: it is a per-device viewing preference, the same class of thing
 * as volume or the chosen subtitle language, and storing it server-side would mean a mutation and a
 * GraphQL field for something the browser already has somewhere to put.
 *
 * `caption+srtt` and `caption+vtt` for the same language share an offset, hence normalising on the
 * language code alone: a track's timings do not change with its container.
 */
export function useCaptionOffset(languageCode: string | undefined) {
  const [offsetMS, setOffsetMS] = useState<number>(0);

  useEffect(() => {
    if (!languageCode) {
      return;
    }
    const stored = readAll()[languageCode];
    setOffsetMS(typeof stored === "number" && Number.isFinite(stored) ? stored : 0);
  }, [languageCode]);

  const setOffset = useCallback(
    (ms: number) => {
      if (!languageCode) {
        return;
      }

      const next = Number.isFinite(ms) ? Math.round(ms) : 0;
      setOffsetMS(next);

      const all = readAll();
      if (next === 0) {
        delete all[languageCode];
      } else {
        all[languageCode] = next;
      }

      try {
        window.localStorage.setItem(STORAGE_KEY, JSON.stringify(all));
      } catch (e) {
        // A full or unavailable localStorage must not break playback.
      }
    },
    [languageCode]
  );

  return { offsetMS, setOffset };
}
