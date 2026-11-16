// Package importer loads the legacy "Controle Gastos" spreadsheet into the database.
package importer

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
)

// Sheet is a parsed worksheet: row number → column letters → cell text.
type Sheet map[int]map[string]string

func (s Sheet) Get(ref string) string {
	col := strings.TrimRight(ref, "0123456789")
	row, _ := strconv.Atoi(ref[len(col):])
	return s[row][col]
}

// readWorkbook is a minimal .xlsx reader (values only, shared strings resolved).
func readWorkbook(file string) (map[string]Sheet, error) {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string, v any) error {
		f, ok := files[name]
		if !ok {
			return fmt.Errorf("%s ausente no xlsx", name)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return xml.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(v)
	}

	var sst struct {
		SI []struct {
			T string `xml:"t"`
			R []struct {
				T string `xml:"t"`
			} `xml:"r"`
		} `xml:"si"`
	}
	if _, ok := files["xl/sharedStrings.xml"]; ok {
		if err := read("xl/sharedStrings.xml", &sst); err != nil {
			return nil, err
		}
	}
	shared := make([]string, len(sst.SI))
	for i, si := range sst.SI {
		s := si.T
		for _, r := range si.R {
			s += r.T
		}
		shared[i] = s
	}

	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := read("xl/workbook.xml", &wb); err != nil {
		return nil, err
	}
	var rels struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := read("xl/_rels/workbook.xml.rels", &rels); err != nil {
		return nil, err
	}
	target := map[string]string{}
	for _, r := range rels.Rel {
		t := strings.TrimPrefix(r.Target, "/")
		if !strings.HasPrefix(t, "xl/") {
			t = path.Join("xl", t)
		}
		target[r.ID] = t
	}

	out := map[string]Sheet{}
	for _, s := range wb.Sheets {
		var ws struct {
			Rows []struct {
				R     int `xml:"r,attr"`
				Cells []struct {
					Ref string `xml:"r,attr"`
					T   string `xml:"t,attr"`
					V   string `xml:"v"`
					IS  struct {
						T string `xml:"t"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := read(target[s.RID], &ws); err != nil {
			return nil, fmt.Errorf("aba %s: %w", s.Name, err)
		}
		sheet := Sheet{}
		for _, row := range ws.Rows {
			cells := map[string]string{}
			for _, c := range row.Cells {
				v := c.V
				switch c.T {
				case "s":
					if i, err := strconv.Atoi(v); err == nil && i < len(shared) {
						v = shared[i]
					}
				case "inlineStr":
					v = c.IS.T
				}
				if v == "" {
					continue
				}
				col := strings.TrimRight(c.Ref, "0123456789")
				cells[col] = v
			}
			if len(cells) > 0 {
				sheet[row.R] = cells
			}
		}
		out[s.Name] = sheet
	}
	return out, nil
}

// excelDate converts an Excel serial date ("46027" or "46027.0") to a date.
func excelDate(s string) (time.Time, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 1 {
		return time.Time{}, false
	}
	return time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(f)), true
}

func cents(s string) (int64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	if f < 0 {
		return int64(f*100 - 0.5), true
	}
	return int64(f*100 + 0.5), true
}
