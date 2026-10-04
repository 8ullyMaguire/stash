import { Maybe } from "src/core/generated-graphql";

// returns true if the file should be treated as a video in the UI
export function isVideo(o: {
  __typename?: string;
  video_codec?: Maybe<string>;
}) {
  return o.__typename === "VideoFile" && o.video_codec !== "gif";
}

// #4233 -- the dimensions a viewer actually sees.
//
// `width` and `height` on a VideoFile are the ENCODED dimensions, straight from the container, and
// they stay that way deliberately: the transcode scale filter and the sprite sheet geometry both read
// them, and swapping them on the server would make those compute against a size the file does not
// have. A phone video held in portrait is therefore stored as 1920x1080 plus a 90-degree rotation
// sidecar, and comparing width against height directly classifies it as landscape -- the player lays
// it out letterboxed inside a portrait viewport, and the player's own portrait styling disagrees with
// what is on screen.
//
// So: use these two for anything user-facing (orientation, aspect ratio, layout), and keep the raw pair
// for anything describing the file itself.
//
// 90 and 270 swap the axes; 0 and 180 do not, which is why this is not "is the rotation odd". The
// value is normalised first because ffprobe may report outside 0..359 and uses a NEGATIVE angle for
// counter-clockwise, which would otherwise fall through to "no swap".
function swapsAxes(rotation: Maybe<number> | undefined) {
  const normalised = (((rotation ?? 0) % 360) + 360) % 360;
  return normalised === 90 || normalised === 270;
}

// The shape both helpers accept. `rotation` is optional so a pre-#4233 server response -- or any
// caller with a partial file object -- still works, defaulting to no rotation.
type DimensionedFile = {
  width?: Maybe<number>;
  height?: Maybe<number>;
  rotation?: Maybe<number>;
};

// Both return undefined when the size is unknown, rather than transposing a zero into the other axis.
// A partial file (width known, height not) would otherwise report a plausible-looking non-zero
// dimension that an aspect-ratio calculation then divides by. This mirrors
// models.VideoFile.DisplayWidth on the Go side.
export function displayWidth(file: DimensionedFile): number | undefined {
  const { width, height } = file;
  if (!width || !height || width <= 0 || height <= 0) {
    return undefined;
  }
  return swapsAxes(file.rotation) ? height : width;
}

export function displayHeight(file: DimensionedFile): number | undefined {
  const { width, height } = file;
  if (!width || !height || width <= 0 || height <= 0) {
    return undefined;
  }
  return swapsAxes(file.rotation) ? width : height;
}

// isPortrait, corrected for the rotation sidecar.
//
// This is deliberately `displayHeight() > displayWidth()` and NOT the stored `height > width`: for a
// quarter turn the display pair is the encoded pair transposed, so the stored comparison is the same
// test in different words and would answer "landscape" for a portrait phone video. Only the display
// pair consults the rotation. Mirrors models.VideoFile.DisplayOrientation.
//
// Missing or zero dimensions are NOT portrait. An unknown size is not evidence either way, and
// guessing would flip the layout of every scene whose metadata failed to scan.
export function isPortraitVideo(file?: DimensionedFile | null): boolean {
  if (!file) {
    return false;
  }

  const width = displayWidth(file);
  const height = displayHeight(file);
  if (width === undefined || height === undefined) {
    return false;
  }
  return height > width;
}

export function isLandscapeVideo(file?: DimensionedFile | null): boolean {
  if (!file) {
    return false;
  }

  const width = displayWidth(file);
  const height = displayHeight(file);
  if (width === undefined || height === undefined) {
    return false;
  }
  return width > height;
}
