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

func newShowCommand() *cobra.Command {
	var withPhone bool
	cmd := &cobra.Command{
		Use:   "show <advert URL or slug>",
		Short: "Show one advert in full: description, features, photos, seller",
		Long: `Show one advert in full: description, features, photos, seller.

Pass the advert's URL, or the slug (<id>_<name>) from search output. The id
alone is not enough: unegui.mn looks adverts up by the whole slug.

--phone also fetches the seller's phone number. The site shows it without a
sign-in, but counts every request as a "phone view" the seller can see, so
ask for it only for adverts you mean to call about.`,
		Example: `  uneguictl show https://www.unegui.mn/adv/10828312_apple-iphone-15-pro-256-gb-natural-titanium/
  uneguictl show 10828312_apple-iphone-15-pro-256-gb-natural-titanium --phone`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, err := unegui.AdvertSlug(args[0])
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			a, err := c.Advert(cmd.Context(), slug)
			if err != nil {
				return err
			}
			if withPhone {
				if a.Phone, err = c.Phone(cmd.Context(), a.ID); err != nil {
					return err
				}
			}
			return emit(cmd, a, func(w io.Writer) error { return printAdvert(w, a) })
		},
	}
	cmd.Flags().BoolVar(&withPhone, "phone", false, "also fetch the seller's phone number (counted as a phone view)")
	return cmd
}

func printAdvert(w io.Writer, a *unegui.Advert) error {
	price := a.PriceText
	if a.PriceNote != "" {
		price = strings.TrimSpace(price + " (" + a.PriceNote + ")")
	}
	seller := a.Seller.Name
	var tags []string
	if a.Seller.Business {
		tags = append(tags, "business")
	}
	if a.Seller.Verified {
		tags = append(tags, "verified")
	}
	if a.SellerAdverts != "" {
		tags = append(tags, a.SellerAdverts)
	}
	if len(tags) > 0 {
		seller += " (" + strings.Join(tags, ", ") + ")"
	}
	status := ""
	if a.Closed {
		status = "closed"
	}
	fields := []output.Field{
		{Name: "Title", Value: a.Title},
		{Name: "Status", Value: status},
		{Name: "Price", Value: price},
		{Name: "Category", Value: strings.Join(a.Category, " › ")},
		{Name: "Location", Value: a.Location},
		{Name: "Posted", Value: a.Published},
		{Name: "Views", Value: strconv.Itoa(a.Views)},
		{Name: "Seller", Value: seller},
		{Name: "Phone", Value: a.Phone},
		{Name: "URL", Value: a.URL},
	}
	for _, f := range a.Features {
		fields = append(fields, output.Field{Name: f.Name, Value: f.Value})
	}
	if a.Description != "" {
		fields = append(fields, output.Field{Name: "Description", Value: a.Description})
	}
	if len(a.Images) > 0 {
		fields = append(fields, output.Field{Name: fmt.Sprintf("Photos (%d)", len(a.Images)), Value: strings.Join(a.Images, "\n")})
	}
	return output.Fields(w, fields)
}
