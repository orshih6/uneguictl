package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/orshih6/uneguictl/internal/output"
	"github.com/orshih6/uneguictl/internal/unegui"
)

func newCategoriesCommand() *cobra.Command {
	var depth int
	cmd := &cobra.Command{
		Use:     "categories [category]",
		Aliases: []string{"cats"},
		Short:   "List categories and their paths, for search --category",
		Long: `List categories and their paths, for search --category.

With no argument, prints the site's category tree (--depth levels of it).
With a category path or URL, prints the categories directly under it with
how many adverts each holds; this is how to reach the deepest levels, such
as a single phone model.`,
		Example: `  uneguictl categories
  uneguictl categories --depth 1
  uneguictl categories utas--dugaar/gar-utas-ba-utasnuud/iphone`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				return subcategories(cmd, c, args[0])
			}
			if depth < 1 || depth > 3 {
				return fmt.Errorf("--depth must be 1, 2 or 3")
			}
			tree, err := c.Categories(cmd.Context())
			if err != nil {
				return err
			}
			tree = prune(tree, depth)
			return emit(cmd, tree, func(w io.Writer) error {
				var rows [][]string
				var walk func(cs []unegui.Category, level int)
				walk = func(cs []unegui.Category, level int) {
					for _, ct := range cs {
						rows = append(rows, []string{strings.Repeat("  ", level) + ct.Name, strconv.Itoa(ct.Count), ct.Path})
						walk(ct.Children, level+1)
					}
				}
				walk(tree, 0)
				return output.Table(w, []string{"CATEGORY", "ADVERTS", "PATH"}, rows)
			})
		},
	}
	cmd.Flags().IntVar(&depth, "depth", 2, "how many levels of the tree to print (1-3)")
	return cmd
}

func subcategories(cmd *cobra.Command, c *unegui.Client, category string) error {
	page, err := c.Search(cmd.Context(), unegui.Query{Category: category, PageSize: 1})
	if err != nil {
		return err
	}
	subs := page.Subcategories
	if subs == nil {
		subs = []unegui.Subcategory{}
	}
	if !flagJSON && len(subs) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s (%d adverts) has no narrower categories.\n", page.Title, page.Count)
	}
	return emit(cmd, subs, func(w io.Writer) error {
		if len(subs) == 0 {
			return nil
		}
		rows := make([][]string, 0, len(subs))
		for _, s := range subs {
			rows = append(rows, []string{s.Name, strconv.Itoa(s.Count), s.Path})
		}
		return output.Table(w, []string{"CATEGORY", "ADVERTS", "PATH"}, rows)
	})
}

// prune cuts the tree below depth levels.
func prune(cs []unegui.Category, depth int) []unegui.Category {
	out := make([]unegui.Category, len(cs))
	for i, c := range cs {
		if depth <= 1 {
			c.Children = nil
		} else {
			c.Children = prune(c.Children, depth-1)
		}
		out[i] = c
	}
	return out
}
