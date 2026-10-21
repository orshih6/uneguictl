package cli

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orshih6/uneguictl/internal/output"
)

func newFiltersCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "filters <category>",
		Short: "List a category's filters, for search --filter",
		Long: `List a category's filters, for search --filter.

Each filter has a slug and values. Pass them to search as
--filter slug=value (several values: slug=1,2). A range filter such as
year or mileage takes slug_min=… and slug_max=… instead; price has its own
--min and --max.`,
		Example: `  uneguictl filters utas--dugaar/gar-utas-ba-utasnuud/iphone
  uneguictl search -c utas--dugaar/gar-utas-ba-utasnuud/iphone -f attrs__bagtaamzh=7,12`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			fs, err := c.Filters(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, fs, func(w io.Writer) error {
				rows := make([][]string, 0, len(fs))
				for _, f := range fs {
					slug, values := f.Slug, ""
					if f.Range {
						slug = f.Slug + "_min / " + f.Slug + "_max"
						if f.Unit != "" {
							values = "number, " + f.Unit
						} else {
							values = "number"
						}
					} else {
						parts := make([]string, 0, len(f.Choices))
						for _, ch := range f.Choices {
							parts = append(parts, ch.Value+"="+ch.Label)
						}
						values = strings.Join(parts, "  ")
					}
					rows = append(rows, []string{f.Name, slug, values})
				}
				return output.Table(w, []string{"FILTER", "SLUG", "VALUES"}, rows)
			})
		},
	}
}
