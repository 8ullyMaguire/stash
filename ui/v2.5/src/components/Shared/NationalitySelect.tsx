import React from "react";
import Select from "react-select";
import { useIntl } from "react-intl";
import { useQuery } from "@apollo/client";
import * as GQL from "src/core/generated-graphql";
import { PatchComponent } from "src/patch";

interface INationality {
  id: string;
  name: string;
  code?: string | null;
}

interface IProps {
  /** Selected nationality ids. Empty or undefined means "not set", not "no nationalities". */
  value?: string[];
  onChange?: (value: string[]) => void;
  disabled?: boolean;
  className?: string;
  menuPortalTarget?: HTMLElement | null;
}

const NationalitySelectQuery = GQL.AllNationalitiesDocument;

/**
 * A multi-select over the nationality reference list. stash#2359, #1922.
 *
 * WHY MULTI, AND WHY NOT CountrySelect
 * ====================================
 *
 * CountrySelect is single-valued because a performer has one country. A performer may hold SEVERAL
 * nationalities -- that is the whole of #1922 -- so this takes an array and is built on react-select
 * directly rather than reusing CountrySelect.
 *
 * AND NOT Creatable
 * =================
 *
 * CountrySelect is a Creatable, because a country is a free-text value and a user must be able to
 * type one this build has never heard of. A nationality here is a REFERENCE ROW with an id, a name
 * and a code, and the reference list is seeded by migration 125 -- 107 entries, fixed. Allowing a
 * user to type a nationality that is not in it would create an id the backend cannot resolve, which
 * `relatedNationalities` rejects by name rather than silently dropping. The trade is deliberate: a
 * nationality Stash-Box knows and this build's seed does not cannot be selected, and that is better
 * than a selection that fails to save.
 *
 * The code is shown alongside the name where there is one, because "Basque" and "Kurdish" have no
 * ISO code and the two NULL-code entries are the ones a user is most likely to be unsure about.
 */
const _NationalitySelect: React.FC<IProps> = ({
  value,
  onChange,
  disabled = false,
  className,
  menuPortalTarget,
}) => {
  const intl = useIntl();

  // Typed explicitly rather than left to inference: useQuery's default generics do not flow the
  // document's own result type through `data` here, so an untyped `n` is an implicit any and the
  // field names go unchecked.
  const { data } = useQuery<GQL.AllNationalitiesQuery>(NationalitySelectQuery);
  const nationalities: INationality[] = data?.allNationalities ?? [];

  const options: { value: string; label: string; name: string }[] =
    nationalities.map((n) => ({
      value: n.id,
      // The name alone, so filtering matches what a user would type. The code is decoration in the
      // label rather than part of the searchable text.
      label: n.code ? `${n.name} (${n.code})` : n.name,
      name: n.name,
    }));

  const selected = options.filter((o) => value?.includes(o.value));

  return (
    <Select
      classNamePrefix="react-select"
      isMulti
      isClearable
      value={selected}
      options={options}
      onChange={(newValue) => onChange?.((newValue ?? []).map((o) => o.value))}
      isDisabled={disabled || !onChange}
      placeholder={intl.formatMessage({ id: "nationality" })}
      noOptionsMessage={() =>
        intl.formatMessage({ id: "nationality_none_found" })
      }
      components={{
        IndicatorSeparator: null,
      }}
      className={`NationalitySelect ${className}`}
      menuPortalTarget={menuPortalTarget}
    />
  );
};

export const NationalitySelect = PatchComponent(
  "NationalitySelect",
  _NationalitySelect
);
