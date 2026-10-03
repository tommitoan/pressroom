package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tommitoan/pressroom/internal/render"
)

// Limits and defaults of the v1 contract.
const (
	maxTemplateBytes = 16 << 10
	minScale         = 0.5
	maxScale         = 2
	maxMarginMM      = 50
)

var papers = map[string]bool{"A4": true, "A3": true, "Letter": true, "Legal": true}

type pdfRequest struct {
	HTML    string      `json:"html"`
	Options *pdfOptions `json:"options"`
}

type pdfOptions struct {
	Paper             *string      `json:"paper"`
	Landscape         *bool        `json:"landscape"`
	MarginsMM         *marginsJSON `json:"margins_mm"`
	PrintBackground   *bool        `json:"print_background"`
	PreferCSSPageSize *bool        `json:"prefer_css_page_size"`
	Scale             *float64     `json:"scale"`
	HeaderHTML        *string      `json:"header_html"`
	FooterHTML        *string      `json:"footer_html"`
}

type marginsJSON struct {
	Top    *float64 `json:"top"`
	Right  *float64 `json:"right"`
	Bottom *float64 `json:"bottom"`
	Left   *float64 `json:"left"`
}

// decodeRequest reads and validates a /v1/pdf body, applying the contract's defaults.
func decodeRequest(r io.Reader) (render.Request, *apiError) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var in pdfRequest
	if err := dec.Decode(&in); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return render.Request{}, errPayloadTooLarge()
		}
		return render.Request{}, errInvalidJSON()
	}
	if dec.More() {
		return render.Request{}, errInvalidJSON()
	}
	// Anything after the object, even a second value, is a malformed body.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return render.Request{}, errPayloadTooLarge()
		}
		return render.Request{}, errInvalidJSON()
	}

	if strings.TrimSpace(in.HTML) == "" {
		return render.Request{}, &apiError{Status: http.StatusBadRequest, Code: "html_required", Message: "html is required"}
	}
	opts, aerr := in.options()
	if aerr != nil {
		return render.Request{}, aerr
	}
	return render.Request{HTML: in.HTML, Options: opts}, nil
}

// options applies defaults and checks every range.
func (in pdfRequest) options() (render.Options, *apiError) {
	opts := render.Options{
		Paper:             "A4",
		PrintBackground:   true,
		PreferCSSPageSize: true,
		Scale:             1,
		Margins:           render.Margins{Top: 15, Right: 12, Bottom: 18, Left: 12},
	}
	o := in.Options
	if o == nil {
		return opts, nil
	}
	if o.Paper != nil {
		if !papers[*o.Paper] {
			return opts, invalidOptions("paper must be one of A4, A3, Letter, Legal")
		}
		opts.Paper = *o.Paper
	}
	if o.Landscape != nil {
		opts.Landscape = *o.Landscape
	}
	if o.PrintBackground != nil {
		opts.PrintBackground = *o.PrintBackground
	}
	if o.PreferCSSPageSize != nil {
		opts.PreferCSSPageSize = *o.PreferCSSPageSize
	}
	if o.Scale != nil {
		if *o.Scale < minScale || *o.Scale > maxScale {
			return opts, invalidOptions("scale must be between 0.5 and 2")
		}
		opts.Scale = *o.Scale
	}
	if m := o.MarginsMM; m != nil {
		for _, f := range []struct {
			name string
			in   *float64
			out  *float64
		}{
			{"top", m.Top, &opts.Margins.Top}, {"right", m.Right, &opts.Margins.Right},
			{"bottom", m.Bottom, &opts.Margins.Bottom}, {"left", m.Left, &opts.Margins.Left},
		} {
			if f.in == nil {
				continue
			}
			if *f.in < 0 || *f.in > maxMarginMM {
				return opts, invalidOptions("margins_mm." + f.name + " must be between 0 and 50")
			}
			*f.out = *f.in
		}
	}
	for _, t := range []struct {
		name string
		in   *string
		out  *string
	}{{"header_html", o.HeaderHTML, &opts.HeaderHTML}, {"footer_html", o.FooterHTML, &opts.FooterHTML}} {
		if t.in == nil {
			continue
		}
		if len(*t.in) > maxTemplateBytes {
			return opts, invalidOptions(t.name + " must be at most 16 KiB")
		}
		*t.out = *t.in
	}
	return opts, nil
}
