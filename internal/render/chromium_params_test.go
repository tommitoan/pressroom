package render

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func defaultOptions() Options {
	return Options{
		Paper:             "A4",
		Margins:           Margins{Top: 15, Right: 12, Bottom: 18, Left: 12},
		PrintBackground:   true,
		PreferCSSPageSize: true,
		Scale:             1,
	}
}

func paramsJSON(t *testing.T, o Options) map[string]any {
	t.Helper()
	p, err := printParams(o)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func near(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	got, ok := m[key].(float64)
	if !ok || math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", key, m[key], want)
	}
}

func TestPrintParamsPaperSizes(t *testing.T) {
	for paper, size := range map[string][2]float64{
		"A4": {8.27, 11.69}, "A3": {11.69, 16.54}, "Letter": {8.5, 11}, "Legal": {8.5, 14},
	} {
		o := defaultOptions()
		o.Paper = paper
		m := paramsJSON(t, o)
		near(t, m, "paperWidth", size[0])
		near(t, m, "paperHeight", size[1])
	}
}

func TestPrintParamsConvertsMarginsToInches(t *testing.T) {
	m := paramsJSON(t, defaultOptions())
	near(t, m, "marginTop", 15/25.4)
	near(t, m, "marginRight", 12/25.4)
	near(t, m, "marginBottom", 18/25.4)
	near(t, m, "marginLeft", 12/25.4)
}

func TestPrintParamsFlags(t *testing.T) {
	o := defaultOptions()
	o.Landscape = true
	o.PrintBackground = false
	o.PreferCSSPageSize = false
	o.Scale = 1.5
	m := paramsJSON(t, o)
	near(t, m, "scale", 1.5)
	if m["landscape"] != true {
		t.Error("landscape not set")
	}
	if m["printBackground"] == true {
		t.Error("printBackground should be off")
	}
	if m["preferCSSPageSize"] == true {
		t.Error("preferCSSPageSize should be off")
	}
}

func TestPrintParamsZeroScaleMeansOne(t *testing.T) {
	o := defaultOptions()
	o.Scale = 0
	near(t, paramsJSON(t, o), "scale", 1)
}

func TestPrintParamsNoHeaderFooterByDefault(t *testing.T) {
	m := paramsJSON(t, defaultOptions())
	if m["displayHeaderFooter"] == true {
		t.Error("header and footer must be off when none was given")
	}
}

func TestPrintParamsFooterOnlyBlanksTheHeader(t *testing.T) {
	o := defaultOptions()
	o.FooterHTML = `<span class="pageNumber"></span>`
	m := paramsJSON(t, o)
	if m["displayHeaderFooter"] != true {
		t.Fatal("displayHeaderFooter not set")
	}
	if m["headerTemplate"] != emptyTemplate {
		t.Errorf("header = %v, want the empty template so Chromium prints no default banner", m["headerTemplate"])
	}
	if !strings.Contains(m["footerTemplate"].(string), "pageNumber") {
		t.Errorf("footer = %v", m["footerTemplate"])
	}
}

func TestPrintParamsHeaderOnlyBlanksTheFooter(t *testing.T) {
	o := defaultOptions()
	o.HeaderHTML = "<b>Title</b>"
	m := paramsJSON(t, o)
	if m["footerTemplate"] != emptyTemplate || m["headerTemplate"] != "<b>Title</b>" {
		t.Errorf("unexpected templates: %v / %v", m["headerTemplate"], m["footerTemplate"])
	}
}

func TestPrintParamsRejectsUnknownPaper(t *testing.T) {
	o := defaultOptions()
	o.Paper = "B5"
	if _, err := printParams(o); err == nil {
		t.Error("an unknown paper must be an error")
	}
}
