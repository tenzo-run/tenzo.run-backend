package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"

	"tenzo-run-backend/internal/lead"
)

const (
	leadTimeLayout = "2006-01-02 15:04 UTC"
	noLeads        = "no leads yet"
	emptyCell      = "-"
)

func printLatestLeads(ctx context.Context, svc *lead.Service, limit int, w io.Writer) error {
	leads, err := svc.Latest(ctx, limit)
	if err != nil {
		return err
	}

	if len(leads) == 0 {
		_, err := fmt.Fprintln(w, noLeads)
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tNAME\tCONTACT\tLOCALE\tPAGE")
	for _, l := range leads {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			l.CreatedAt.UTC().Format(leadTimeLayout), cell(l.Name), cell(l.Contact), l.Locale, cell(l.Page))
	}
	return tw.Flush()
}

func cell(s string) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsControl), " ")
	if s == "" {
		return emptyCell
	}
	return s
}
