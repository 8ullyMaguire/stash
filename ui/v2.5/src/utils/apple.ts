import { UAParser } from "ua-parser-js";

export function isPlatformUniquelyRenderedByApple(): boolean {
  // OS name on iPads show up as iOS or macOS depending on the browser, and
  // ua-parser-js v2 reports "macOS" where v1 reported "Mac OS" -- so match both.
  // Matching only "Mac OS" is what made every desktop Safari user miss out: the
  // string never occurs in v2, so the function returned false, the `apple` class
  // was never applied, and .detail-container never got display: flex -- which is
  // exactly the "details shown under the picture" report in stash#7234.
  //
  // One parse, not two. The previous version called UAParser() twice with no
  // argument, which reads navigator.userAgent and builds a fresh parser each
  // time. The two reads cannot actually disagree -- userAgent does not change
  // during a page's life -- but taking os and browser from ONE parse says
  // plainly that they describe the same browser.
  const { os, browser } = UAParser();

  const osName = os.name?.toLowerCase() ?? "";
  const isIOS = osName.includes("ios");
  // An iPad in desktop mode reports as macOS with a Safari browser, which is the
  // same rendering quirk this class exists to correct.
  const isMacOS = osName.includes("mac os") || osName.includes("macos");
  const isSafari = browser.name?.includes("Safari") ?? false;

  return isIOS || (isMacOS && isSafari);
}
