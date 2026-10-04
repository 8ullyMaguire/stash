-- stash#2359 follow-up: complete the nationality reference list.
--
-- WHY THIS EXISTS, AND WHAT IT IS NOT
-- ===================================
--
-- An earlier comment on `type Nationality` said this list mirrors Stash-Box, which "carries
-- nationalities that are not countries". Checked against the Stash-Box schema on 2026-10-04 that was
-- false -- Performer there has `country: String` and `ethnicity: EthnicityEnum` and no nationalities
-- field at all -- and that comment has been corrected. The table is not a mirror, so the honest
-- question is what it is FOR, and the answer is upstream #1922:
--
--   7dJx1iP: "In my dataset I have a lot of performers with multiple comma separated nationalities
--             stored in the country field."
--   ghost:   "Not entirely sure what you mean by that? A separate field for nationality distinct
--             from country?"
--
-- One free-text string cannot hold several nationalities, and #1922's proposed fix -- store an ISO
-- code instead -- makes that loss permanent rather than repairing it. #1922 was closed UNMERGED, so
-- upstream `develop` still has `country: String` and a multi-valued nationality field conflicts with
-- nothing. This list is the answer to a question Stash-Box leaves open.
--
-- SO WHY AN INSERT RATHER THAN A CREATE MUTATION
-- ==============================================
--
-- `NationalitySelect` is deliberately not Creatable (see the component's own comment: a country is
-- free text and needs a Creatable, a nationality is a fixed reference list and does not). Without a
-- way to add a row at runtime, an incomplete list is a PERMANENT hole -- a performer whose
-- nationality is absent can never be recorded at all, and no amount of exporting and re-importing
-- fixes it. So the list has to be complete by construction, and this migration is the only place
-- that can happen.
--
-- THE NINE CANDIDATES, AND WHY EIGHT SHIPPED
-- ===========================================
--
-- Probing the 107-row list against a wide demonym set found nine absent entries. Eight ship. One
-- does not:
--
--   'Croat' -- DROPPED. HR is already taken by 'Croatian', and this list carries ONE demonym per
--              country. Seeding both 'Croat' and 'Croatian' for HR gives the same country two names,
--              which is the exact ambiguity that made #1922 hard to do in the first place: a
--              free-text country field cannot tell you whether 'Croat' and 'Croatian' meant the same
--              entry. Reference data with a duplicate name recreates the problem it exists to
--              solve.
--
-- Also rejected during the probe: 'Iran' and 'Singapore'. Those are COUNTRY NAMES that my probe
-- listed as demonyms by mistake, and the list already carries 'Iranian' and 'Singaporean'. Seeding
-- a country name beside its demonym is the same duplicate-name hazard as 'Croat', so they are not
-- errors to fix -- they are probe noise, and fixing them would have introduced the defect.
--
-- XK for Kosovar
-- =============
--
-- XK is the user-assigned code for Kosovo. It is not in ISO 3166-1, so `code` here is documented as
-- "ISO 3166-1 alpha-2, or null for a nationality that is not a country" and XK needs a sentence of
-- its own rather than a silent exception. It is not a country, but it HAS a widely used code, so NULL
-- would discard a real value; Basque and Kurdish keep NULL because they have no code in common use.
--
-- IDEMPOTENCE AND RE-RUN SAFETY
-- =============================
--
-- `WHERE NOT EXISTS` on the name means this is safe to apply to a database that somehow already has
-- one of these rows, and makes a duplicate name impossible even if this file were applied twice.

INSERT INTO `nationalities` (`name`, `code`)
  SELECT 'Armenian', 'AM'    WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Armenian')
UNION ALL SELECT 'Ghanaian',   'GH'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Ghanaian')
UNION ALL SELECT 'Guyanese',   'GY'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Guyanese')
UNION ALL SELECT 'Kosovar',    'XK'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Kosovar')
UNION ALL SELECT 'Montenegrin','ME'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Montenegrin')
UNION ALL SELECT 'Sri Lankan', 'LK'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Sri Lankan')
UNION ALL SELECT 'Surinamese', 'SR'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Surinamese')
UNION ALL SELECT 'Uzbek',      'UZ'   WHERE NOT EXISTS (SELECT 1 FROM `nationalities` WHERE `name` = 'Uzbek');