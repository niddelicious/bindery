package bnf

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// sruResponse is an SRU 1.2 searchRetrieveResponse. Element names are matched
// on their local name, so the srw: prefix BnF uses does not matter, and every
// field is optional: a response missing one decodes to its zero value.
type sruResponse struct {
	XMLName         xml.Name        `xml:"searchRetrieveResponse"`
	NumberOfRecords string          `xml:"numberOfRecords"`
	Records         []sruRecord     `xml:"records>record"`
	Diagnostics     []sruDiagnostic `xml:"diagnostics>diagnostic"`
}

// total is numberOfRecords, or -1 when the response does not state a usable
// count.
func (r *sruResponse) total() int {
	n, err := strconv.Atoi(strings.TrimSpace(r.NumberOfRecords))
	if err != nil || n < 0 {
		return -1
	}
	return n
}

type sruRecord struct {
	Data struct {
		Record marcRecord `xml:"record"`
	} `xml:"recordData"`
}

// sruDiagnostic is an SRU error. BnF sends one with HTTP 200, for example
// "Index non supporté" for an unknown index.
type sruDiagnostic struct {
	URI     string `xml:"uri"`
	Details string `xml:"details"`
	Message string `xml:"message"`
}

// marcRecord is one marcxchange record in UNIMARC, the schema Bindery asks
// for (recordSchema=unimarcXchange). Type is "Bibliographic" or "Authority";
// ID is the record's ARK ("ark:/12148/cb...").
type marcRecord struct {
	ID            string         `xml:"id,attr"`
	Type          string         `xml:"type,attr"`
	Format        string         `xml:"format,attr"`
	Leader        string         `xml:"leader"`
	ControlFields []controlField `xml:"controlfield"`
	DataFields    []dataField    `xml:"datafield"`
}

type controlField struct {
	Tag   string `xml:"tag,attr"`
	Value string `xml:",chardata"`
}

type dataField struct {
	Tag       string     `xml:"tag,attr"`
	Ind1      string     `xml:"ind1,attr"`
	Ind2      string     `xml:"ind2,attr"`
	Subfields []subfield `xml:"subfield"`
}

type subfield struct {
	Code  string `xml:"code,attr"`
	Value string `xml:",chardata"`
}

func (r marcRecord) controlField(tag string) string {
	for _, cf := range r.ControlFields {
		if cf.Tag == tag {
			return strings.TrimSpace(cf.Value)
		}
	}
	return ""
}

func (r marcRecord) fields(tag string) []dataField {
	var out []dataField
	for _, df := range r.DataFields {
		if df.Tag == tag {
			out = append(out, df)
		}
	}
	return out
}

// first returns the first $code of the first field tagged tag that has one.
func (r marcRecord) first(tag, code string) string {
	for _, df := range r.DataFields {
		if df.Tag != tag {
			continue
		}
		if v := df.first(code); v != "" {
			return v
		}
	}
	return ""
}

// all returns every $code value across every field tagged tag.
func (r marcRecord) all(tag, code string) []string {
	var out []string
	for _, df := range r.DataFields {
		if df.Tag == tag {
			out = append(out, df.all(code)...)
		}
	}
	return out
}

func (df dataField) first(code string) string {
	for _, sf := range df.Subfields {
		if sf.Code == code {
			if v := strings.TrimSpace(sf.Value); v != "" {
				return v
			}
		}
	}
	return ""
}

func (df dataField) all(code string) []string {
	var out []string
	for _, sf := range df.Subfields {
		if sf.Code == code {
			if v := strings.TrimSpace(sf.Value); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}
