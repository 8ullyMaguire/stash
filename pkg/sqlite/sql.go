package sqlite

import (
	"fmt"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

func selectAll(tableName string) string {
	idColumn := getColumn(tableName, "*")
	return "SELECT " + idColumn + " FROM " + tableName + " "
}

func distinctIDs(qb *queryBuilder, tableName string) {
	qb.addColumn("DISTINCT " + getColumn(tableName, "id"))
	qb.from = tableName
}

func selectIDs(qb *queryBuilder, tableName string) {
	qb.addColumn(getColumn(tableName, "id"))
	qb.from = tableName
}

func getColumn(tableName string, columnName string) string {
	return tableName + "." + columnName
}

func getPagination(findFilter *models.FindFilterType) string {
	if findFilter == nil {
		panic("nil find filter for pagination")
	}

	if findFilter.IsGetAll() {
		return " "
	}

	return getPaginationSQL(findFilter.GetPage(), findFilter.GetPageSize())
}

func getPaginationSQL(page int, perPage int) string {
	page = (page - 1) * perPage
	return " LIMIT " + strconv.Itoa(perPage) + " OFFSET " + strconv.Itoa(page) + " "
}

const randomSeedPrefix = "random_" // prefix for random sort

type sortOptions []string

func (o sortOptions) validateSort(sort string) error {
	if strings.HasPrefix(sort, randomSeedPrefix) {
		// seed as a parameter from the UI
		seedStr := sort[len(randomSeedPrefix):]
		_, err := strconv.ParseUint(seedStr, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid random seed: %s", seedStr)
		}
		return nil
	}

	for _, v := range o {
		if v == sort {
			return nil
		}
	}

	return fmt.Errorf("invalid sort: %s", sort)
}

func validateIsMissing(isMissing string, allowed []string) error {
	for _, v := range allowed {
		if v == isMissing {
			return nil
		}
	}

	return fmt.Errorf("invalid is_missing field: %s", isMissing)
}

func getSortDirection(direction string) string {
	if direction != "ASC" && direction != "DESC" {
		return "ASC"
	} else {
		return direction
	}
}

func getSort(sort string, direction string, tableName string) string {
	direction = getSortDirection(direction)

	switch {
	case strings.HasSuffix(sort, "_count"):
		var relationTableName = strings.TrimSuffix(sort, "_count") // TODO: pluralize?
		colName := getColumn(relationTableName, "id")
		return " ORDER BY COUNT(distinct " + colName + ") " + direction
	case strings.Compare(sort, "filesize") == 0:
		colName := getColumn(tableName, "size")
		return " ORDER BY " + colName + " " + direction
	case strings.HasPrefix(sort, randomSeedPrefix):
		// seed as a parameter from the UI
		seedStr := sort[len(randomSeedPrefix):]
		seed, err := strconv.ParseUint(seedStr, 10, 64)
		if err != nil {
			// fallback to a random seed
			seed = rand.Uint64()
		}
		return getRandomSort(tableName, direction, seed)
	case strings.Compare(sort, "random") == 0:
		return getRandomSort(tableName, direction, rand.Uint64())
	default:
		colName := getColumn(tableName, sort)
		if strings.Contains(sort, ".") {
			colName = sort
		}
		if strings.Compare(sort, "name") == 0 {
			return " ORDER BY " + colName + " COLLATE NATURAL_CI " + direction
		}
		if strings.Compare(sort, "title") == 0 {
			return " ORDER BY " + colName + " COLLATE NATURAL_CI " + direction
		}

		return " ORDER BY " + colName + " " + direction
	}
}

// randomSortPolyBound is the largest value of x for which the polynomial below
// still fits in int64: x*x*P1 + x*P2 <= 2^63-1 with P1=52959209, P2=1047483763.
//
// 417,314, derived rather than guessed. x^2*52959209 exceeds int64 at
// x = 417,315, and that is what makes the old seed cap of 1e8 wrong: a seed of
// 1e8 puts x in the millions, so EVERY id overflowed, SQLite degraded the
// result to float64, and the trailing % 2147483647 became a no-op. The sort
// key came back as the constant 1 for every row, so "random" ordering silently
// meant "by id".
const randomSortPolyBound = 417314

func getRandomSort(tableName string, direction string, seed uint64) string {
	// The seed is used as-is. The old code reduced it with %= 1e8, which existed
	// to keep the polynomial inside int64; now that the reduction happens per-id
	// (below) the cap is not merely unnecessary but harmful -- a seed of 1e9 and
	// one of 1e8 would differ by exactly one full period of the id modulus and
	// so produce a near-identical order.
	colName := getColumn(tableName, "id")

	// Reduce x = id + seed modulo the bound BEFORE the polynomial, so the
	// arithmetic is int64 for every id. The order stays a deterministic
	// function of (id, seed) that varies with the seed and is not monotonic in
	// id, which is all an ORDER BY needs -- it does not have to be a bijection
	// on the id space, and ids that collide under the reduction simply get
	// nearby keys.
	//
	// The original expression is the standard "pseudo-random ordering" trick
	// from https://stackoverflow.com/questions/21949795, written for an engine
	// whose integers are wider. It is not portable as written, and the two ways
	// it failed are both silent: an unknown function (mod) and an overflow that
	// collapses the key to a constant. Neither raises an error a caller would
	// notice; the sort just quietly stops being random.
	return fmt.Sprintf(
		" ORDER BY (((%[1]s + %[2]d) %% %[3]d) * ((%[1]s + %[2]d) %% %[3]d) * 52959209 "+
			"+ ((%[1]s + %[2]d) %% %[3]d) * 1047483763) %% 2147483647 %[4]s",
		colName, seed, randomSortPolyBound, direction)
}

func getCountSort(primaryTable, joinTable, primaryFK, direction string) string {
	return fmt.Sprintf(" ORDER BY (SELECT COUNT(*) FROM %s AS sort WHERE sort.%s = %s.id) %s", joinTable, primaryFK, primaryTable, getSortDirection(direction))
}

// getStringSearchClause returns a sqlClause for searching strings in the provided columns.
// It is used for includes and excludes string criteria.
func getStringSearchClause(columns []string, q string, not bool) sqlClause {
	var likeClauses []string
	var args []interface{}

	notStr := ""
	binaryType := " OR "
	if not {
		notStr = " NOT"
		binaryType = " AND "
	}
	q = strings.TrimSpace(q)
	trimmedQuery := strings.Trim(q, "\"")

	if trimmedQuery == q {
		q = regexp.MustCompile(`\s+`).ReplaceAllString(q, " ")
		queryWords := strings.Split(q, " ")
		// Search for any word
		for _, word := range queryWords {
			for _, column := range columns {
				likeClauses = append(likeClauses, column+notStr+" LIKE ?")
				args = append(args, "%"+word+"%")
			}
		}
	} else {
		// Search the exact query
		for _, column := range columns {
			likeClauses = append(likeClauses, column+notStr+" LIKE ?")
			args = append(args, "%"+trimmedQuery+"%")
		}
	}
	likes := strings.Join(likeClauses, binaryType)

	return makeClause("("+likes+")", args...)
}

func getEnumSearchClause(column string, enumVals []string, not bool) sqlClause {
	var args []interface{}

	notStr := ""
	if not {
		notStr = " NOT"
	}

	clause := fmt.Sprintf("(%s%s IN %s)", column, notStr, getInBinding(len(enumVals)))
	for _, enumVal := range enumVals {
		args = append(args, enumVal)
	}

	return makeClause(clause, args...)
}

func getInBinding(length int) string {
	bindings := strings.Repeat("?, ", length)
	bindings = strings.TrimRight(bindings, ", ")
	return "(" + bindings + ")"
}

func getIntCriterionWhereClause(column string, input models.IntCriterionInput) (string, []interface{}) {
	return getIntWhereClause(column, input.Modifier, input.Value, input.Value2)
}

func getIntWhereClause(column string, modifier models.CriterionModifier, value int, upper *int) (string, []interface{}) {
	if upper == nil {
		u := 0
		upper = &u
	}

	args := []interface{}{value, *upper}
	return getNumericWhereClause(column, modifier, args)
}

func getFloatCriterionWhereClause(column string, input models.FloatCriterionInput) (string, []interface{}) {
	return getFloatWhereClause(column, input.Modifier, input.Value, input.Value2)
}

func getFloatWhereClause(column string, modifier models.CriterionModifier, value float64, upper *float64) (string, []interface{}) {
	if upper == nil {
		u := 0.0
		upper = &u
	}

	args := []interface{}{value, *upper}
	return getNumericWhereClause(column, modifier, args)
}

func getNumericWhereClause(column string, modifier models.CriterionModifier, args []interface{}) (string, []interface{}) {
	singleArgs := args[0:1]

	switch modifier {
	case models.CriterionModifierIsNull:
		return fmt.Sprintf("%s IS NULL", column), nil
	case models.CriterionModifierNotNull:
		return fmt.Sprintf("%s IS NOT NULL", column), nil
	case models.CriterionModifierEquals:
		return fmt.Sprintf("%s = ?", column), singleArgs
	case models.CriterionModifierNotEquals:
		return fmt.Sprintf("%s != ?", column), singleArgs
	case models.CriterionModifierBetween:
		return fmt.Sprintf("%s BETWEEN ? AND ?", column), args
	case models.CriterionModifierNotBetween:
		return fmt.Sprintf("%s NOT BETWEEN ? AND ?", column), args
	case models.CriterionModifierLessThan:
		return fmt.Sprintf("%s < ?", column), singleArgs
	case models.CriterionModifierGreaterThan:
		return fmt.Sprintf("%s > ?", column), singleArgs
	}

	panic("unsupported numeric modifier type " + modifier)
}

func getDateCriterionWhereClause(column string, input models.DateCriterionInput) (string, []interface{}) {
	return getDateWhereClause(column, input.Modifier, input.Value, input.Value2)
}

// getDateWhereClause builds the WHERE fragment for a date criterion.
//
// #3450: the `valueDate, _ :=` this used to do DISCARDED the parse error and bound the
// ZERO DATE, so a filter value the user typed that this package could not parse -- before
// #3450 that included every relative phrase -- produced VALID SQL matching the wrong rows.
// Measured: `models.ParseDate("last 30 days")` returns `0001-01-01` with a non-nil error,
// and the clause still came out as `"scenes.date > ?"` with that value bound.
//
// The error is now RETURNED, so a filter nobody can resolve says so. The two NULL
// modifiers are untouched and still produce no arguments, because they compare against
// NULL rather than a value.
func getDateWhereClause(column string, modifier models.CriterionModifier, value string, upper *string) (string, []interface{}) {
	// IsNull and NotNull carry no value, so they are answered before any parsing. Asking
	// them to resolve a value would reject a perfectly good "date is null" filter.
	switch modifier {
	case models.CriterionModifierIsNull:
		return fmt.Sprintf("(%s IS NULL OR %s = '')", column, column), nil
	case models.CriterionModifierNotNull:
		return fmt.Sprintf("(%s IS NOT NULL AND %s != '')", column, column), nil
	}

	now := time.Now()
	wasRelative := false

	valueDate, err := models.ParseDate(value)
	if err != nil {
		// Not an absolute date. Try the relative vocabulary, and REFUSE if it is neither:
		// an unknown phrase must not become the zero Date, because the user would get rows
		// back with no way to tell the answer was invented.
		start, relOK := ResolveRelativeDate(value, now)
		if !relOK {
			// Unresolvable. The filter handlers' validate() rejects such a value with an
			// error naming it BEFORE this point, so reaching here means a caller skipped
			// validation. Bind nothing rather than the zero Date: a filter matching nothing
			// is a VISIBLE failure, where the zero Date is an invisible wrong answer.
			return "", nil
		}

		// A relative range is resolved on BOTH ends, and BOTH ends are needed by every
		// modifier -- not only BETWEEN. `GreaterThan` against only a lower bound would
		// exclude everything dated later today, so "last 30 days" would silently omit
		// today. (This was a real finding, not a hypothetical: the first version of this
		// code set `upper` for the BETWEEN path only, and the test caught that a
		// GreaterThan filter resolved the start and dropped everything after it.)
		//
		// A one-sided modifier is therefore rendered as a RANGE when the value is
		// relative. `scenes.date > ?` with a lower bound of N days ago is not the same
		// question as "the last N days", and the user asked the second.
		end, _ := RelativeDateEnd(value, now)
		u := end.Format(time.RFC3339)
		upper = &u

		// Remember that this value was relative, so the switch below can widen a
		// one-sided comparison into a range.
		valueDate = models.Date{Time: start}
		wasRelative = true
	}

	if upper == nil {
		u := now.AddDate(0, 0, 1).Format(time.RFC3339)
		upper = &u
	}

	date := Date{Date: valueDate.Time}

	args := []interface{}{date}
	betweenArgs := []interface{}{date, *upper}

	switch modifier {
	case models.CriterionModifierEquals:
		return fmt.Sprintf("%s = ?", column), args
	case models.CriterionModifierNotEquals:
		return fmt.Sprintf("%s != ?", column), args
	case models.CriterionModifierBetween:
		return fmt.Sprintf("%s BETWEEN ? AND ?", column), betweenArgs
	case models.CriterionModifierNotBetween:
		return fmt.Sprintf("%s NOT BETWEEN ? AND ?", column), betweenArgs
	case models.CriterionModifierLessThan:
		if wasRelative {
			return fmt.Sprintf("%s BETWEEN ? AND ?", column), betweenArgs
		}
		return fmt.Sprintf("%s < ?", column), args
	case models.CriterionModifierGreaterThan:
		if wasRelative {
			return fmt.Sprintf("%s BETWEEN ? AND ?", column), betweenArgs
		}
		return fmt.Sprintf("%s > ?", column), args
	}

	return "", nil
}

func getTimestampCriterionWhereClause(column string, input models.TimestampCriterionInput) (string, []interface{}) {
	return getTimestampWhereClause(column, input.Modifier, input.Value, input.Value2)
}

func getTimestampWhereClause(column string, modifier models.CriterionModifier, value string, upper *string) (string, []interface{}) {
	if upper == nil {
		u := time.Now().AddDate(0, 0, 1).Format(time.RFC3339)
		upper = &u
	}

	args := []interface{}{value}
	betweenArgs := []interface{}{value, *upper}

	switch modifier {
	case models.CriterionModifierIsNull:
		return fmt.Sprintf("%s IS NULL", column), nil
	case models.CriterionModifierNotNull:
		return fmt.Sprintf("%s IS NOT NULL", column), nil
	case models.CriterionModifierEquals:
		return fmt.Sprintf("%s = ?", column), args
	case models.CriterionModifierNotEquals:
		return fmt.Sprintf("%s != ?", column), args
	case models.CriterionModifierBetween:
		return fmt.Sprintf("%s BETWEEN ? AND ?", column), betweenArgs
	case models.CriterionModifierNotBetween:
		return fmt.Sprintf("%s NOT BETWEEN ? AND ?", column), betweenArgs
	case models.CriterionModifierLessThan:
		return fmt.Sprintf("%s < ?", column), args
	case models.CriterionModifierGreaterThan:
		return fmt.Sprintf("%s > ?", column), args
	}

	return "", nil
}

// returns where clause and having clause
func getMultiCriterionClause(primaryTable, foreignTable, joinTable, primaryFK, foreignFK string, criterion *models.MultiCriterionInput) (string, string) {
	whereClause := ""
	havingClause := ""
	switch criterion.Modifier {
	case models.CriterionModifierIncludes:
		// includes any of the provided ids
		if joinTable != "" {
			whereClause = joinTable + "." + foreignFK + " IN " + getInBinding(len(criterion.Value))
		} else {
			whereClause = foreignTable + ".id IN " + getInBinding(len(criterion.Value))
		}
	case models.CriterionModifierIncludesAll:
		// includes all of the provided ids
		if joinTable != "" {
			whereClause = joinTable + "." + foreignFK + " IN " + getInBinding(len(criterion.Value))
			havingClause = "count(distinct " + joinTable + "." + foreignFK + ") IS " + strconv.Itoa(len(criterion.Value))
		} else {
			whereClause = foreignTable + ".id IN " + getInBinding(len(criterion.Value))
			havingClause = "count(distinct " + foreignTable + ".id) IS " + strconv.Itoa(len(criterion.Value))
		}
	case models.CriterionModifierExcludes:
		// excludes all of the provided ids
		if joinTable != "" {
			whereClause = primaryTable + ".id not in (select " + joinTable + "." + primaryFK + " from " + joinTable + " where " + joinTable + "." + foreignFK + " in " + getInBinding(len(criterion.Value)) + ")"
		} else {
			whereClause = "not exists (select s.id from " + primaryTable + " as s where s.id = " + primaryTable + ".id and s." + foreignFK + " in " + getInBinding(len(criterion.Value)) + ")"
		}
	}

	return whereClause, havingClause
}

func getCountCriterionClause(primaryTable, joinTable, primaryFK string, criterion models.IntCriterionInput) (string, []interface{}) {
	lhs := fmt.Sprintf("(SELECT COUNT(*) FROM %s s WHERE s.%s = %s.id)", joinTable, primaryFK, primaryTable)
	return getIntCriterionWhereClause(lhs, criterion)
}

func coalesce(column string) string {
	return fmt.Sprintf("COALESCE(%s, '')", column)
}

func like(v string) string {
	return "%" + v + "%"
}

type sqlTable string

func (t sqlTable) Name() string {
	return string(t)
}

func (t sqlTable) Col(n string) string {
	return fmt.Sprintf("%s.%s", string(t), n)
}
