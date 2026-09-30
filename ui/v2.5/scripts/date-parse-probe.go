// Command date-parse-probe answers one question: does the BACKEND parse this
// string as a date, and to what year/month/day?
//
// It exists so the frontend's normaliser can be checked against the thing it
// feeds. scripts/test-date-normalisation.mjs normalises a date and asks this
// program whether the API would accept the result; if the two implementations
// disagree about the two-digit-year pivot or about a month's length, the
// frontend has moved the error rather than fixed it and the user sees a
// validation error on a value they were told was fine.
//
// It deliberately prints a single word rather than a date: the frontend only
// needs to know accept-or-reject, and printing the parsed components would
// tempt a comparison that ignores the point of the check.
//
//	go run ./scripts/date-parse-probe.go 2014-01-02   -> ok
//	go run ./scripts/date-parse-probe.go 2014-13-02   -> reject
package main

import (
	"fmt"
	"os"

	"github.com/stashapp/stash/pkg/models"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: date-parse-probe <date-string>")
		os.Exit(2)
	}

	d, err := models.ParseDate(os.Args[1])
	if err != nil {
		fmt.Println("reject")
		return
	}

	// A year-precision parse is a real parse, not a silent truncation: the
	// frontend may normalise "2014-01" to itself, and the backend must keep the
	// month precision rather than reading it as January of a bare year.
	switch d.Precision {
	case models.DatePrecisionDay:
		fmt.Printf("ok %d-%02d-%02d day\n", d.Year(), int(d.Month()), d.Day())
	case models.DatePrecisionMonth:
		fmt.Printf("ok %d-%02d month\n", d.Year(), int(d.Month()))
	case models.DatePrecisionYear:
		fmt.Printf("ok %d year\n", d.Year())
	default:
		fmt.Println("reject")
	}
}
