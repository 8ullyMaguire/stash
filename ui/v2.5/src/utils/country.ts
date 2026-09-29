import Countries from "i18n-iso-countries";
import { getLocaleCode } from "src/locales";

// WHY AN EXPLICIT OVERRIDE -- stash#5237.
//
// The English label for TW was "Taiwan, Province of China", taken from the
// library's OFFICIAL name. The library also stores a second name for TW:
//
//   official: "Taiwan, Province of China"
//   alternate: "Taiwan"
//
// so the wording the issue asks for is not an invention -- it is the other
// name the library already has. This file shows that one.
//
// WHY NOT THE LIBRARY'S select: "alias" OPTION, WHICH LOOKS OBVIOUSLY RIGHT.
//
// `getName(code, "en", {select: "alias"})` returns the first ALTERNATE name.
// That was implemented here, measured, and reverted. It changes far more than
// this issue asks for, and two of its results are outright regressions. All
// 20 codes that have alternate names change under it:
//
//   CI  Cote d'Ivoire              -> Côte d'Ivoire
//   CN  People's Republic of China -> China
//   KR  South Korea                -> Korea, Republic of    <-- WORSE
//   AX  Åland Islands              -> Aland Islands          <-- WORSE
//   TR  Türkiye                    -> Turkey                 <-- reverts a rename
//   US  United States of America   -> United States
//   ... and 14 more.
//
// The KR case is the clearest proof the flag does not mean what it appears
// to: the official name is the readable "South Korea", and alias mode
// replaces it with "Korea, Republic of" -- the ISO-style inverted form the
// official name exists to avoid. AX loses the ring above the A. TR silently
// reverts Türkiye to Turkey, undoing a rename the library made deliberately.
//
// A flag that looks like "prefer the common name" is really "prefer whatever
// the data happens to list second", and for these entries the second entry is
// not the common name. Fixing one label and damaging nineteen is not a
// neutrality fix, it is a side effect. So the override below is explicit and
// limited to what was asked for.
//
// WHAT IS AND IS NOT BEING CLAIMED.
//
// This is narrow and mechanical. The upstream thread is a political flame
// war; the claim here is only that the library already lists "Taiwan" as an
// alternate official name for TW, and that the UI showed the other one. The
// other 19 countries are left exactly as upstream ships them, because an
// instance operator can already change the display locale, and nobody filed a
// report about the other 19.
//
// WHAT THIS DELIBERATELY DOES NOT DO.
//
// It does not special-case CN, which is stored as
// ["People's Republic of China", "China"] and has the identical shape. That is
// a separate decision about a separate country; bundling it into this commit
// is how a naming fix becomes a position. It is one line here if wanted.
//
// It does not touch the alternate names for any other code, for the reasons
// given above.

/**
 * Country names Stash renders differently from the upstream library.
 *
 * Keyed by ISO 3166-1 alpha-2 code.
 *
 * Only entries whose DISPLAYED name changes belong here. The database stores
 * performer.country as an uppercase alpha-2 code, and this map is consulted
 * on every render of the Country dropdown, so an entry is a deliberate,
 * reviewed decision about exactly one country's label.
 */
export const countryNameOverrides: Record<string, string> = {
  // "Taiwan, Province of China" -> "Taiwan". The library's own alternate
  // official name for this code. stash#5237.
  TW: "Taiwan",
};

/**
 * Look up an override, tolerating the casing callers actually pass.
 *
 * The database stores the code uppercase (migration 37_iso_country_names),
 * but this map is consulted from rendering paths that also see values from
 * older records and imported metadata that were never normalised. A lookup
 * that assumed uppercase would miss and silently render the library's name
 * again -- a failure that is invisible in the UI and looks exactly like the
 * override not existing.
 */
const overrideFor = (iso: string): string | undefined =>
  countryNameOverrides[iso.trim().toUpperCase()];

/**
 * Resolve a country code to the name to display.
 *
 * Prefers the override, then the library, and finally English so an unknown
 * locale never renders an empty field.
 */
export const getCountryByISO = (
  iso: string | null | undefined,
  locale: string = "en"
): string | undefined => {
  if (!iso) return;

  const localeCode = getLocaleCode(locale);

  const override = iso && overrideFor(iso);
  if (override) {
    return override;
  }

  const ret = Countries.getName(iso, localeCode);
  if (ret) {
    return ret;
  }

  // fallback to english if locale is not en
  if (locale !== "en") {
    return Countries.getName(iso, "en");
  }
};

export const getCountries = (locale: string = "en") => {
  let countries = Countries.getNames(getLocaleCode(locale));

  if (!countries.length) {
    countries = Countries.getNames("en");
  }

  const entries = Object.entries(countries).map(([code, name]) => {
    // Apply the override in EVERY locale, not only English.
    //
    // The override exists because the name is contentious, and that is not a
    // property of the UI language. A user running Stash in Spanish sees the
    // same database row, and must not see the wording this map exists to
    // prevent, in any locale. Applying it only under `en` would leave the
    // label reachable from every other locale.
    const override = overrideFor(code);
    return { label: override ?? name, value: code };
  });

  return entries;
};
