// Copyright 2020 The covid19 Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/csv"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go-hep.org/x/hep/hplot"
	"gonum.org/v1/gonum/floats"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotutil"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"gonum.org/v1/plot/vg/vgimg"
)

func main() {
	log.SetPrefix("covid19: ")
	log.SetFlags(0)

	http.HandleFunc("/", rootHandle)
	http.HandleFunc("/img-confirmed", imgHandle("confirmed", 100))
	http.HandleFunc("/img-deaths", imgHandle("deaths", 10))
	log.Printf("ready to serve...")
	http.ListenAndServe(":8080", nil)
}

func rootHandle(w http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(w, page)
}

func imgHandle(title string, cutoff float64) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		img, err := genImage(title, cutoff)
		if err != nil {
			log.Printf("error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		err = png.Encode(w, img)
		if err != nil {
			log.Printf("error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		f, err := os.Create("covid-" + strings.ToLower(title) + ".png")
		if err != nil {
			log.Printf("error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		defer f.Close()
		err = png.Encode(f, img)
		if err != nil {
			log.Printf("error: %+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func genImage(title string, cutoff float64) (image.Image, error) {
	countries := []string{
		"France",
		"Italy",
		"Spain",
		//	"Korea, South",
		//	"China",
		"Germany",
		"US",
		"United Kingdom",
	}
	ds, err := fetchData(title, cutoff, countries)
	if err != nil {
		return nil, fmt.Errorf("could not fetch data: %w", err)
	}
	date := ds.date
	dataset := ds.table
	log.Printf("%s: data for %q", title, date.Format("2006-01-02"))

	p := hplot.New()
	p.Title.Text = "CoVid-19 - " + title + " - " + date.Format("2006-01-02")
	p.X.Label.Text = fmt.Sprintf("Days from first %d confirmed cases", int(cutoff))
	p.X.Tick.Marker = hplot.Ticks{N: 20}
	p.Y.Scale = plot.LogScale{}
	p.Y.Tick.Marker = plot.LogTicks{}

	legends := make(map[string]plot.Thumbnailer)
	for i, name := range countries {
		ys := dataset[name]
		xs := make([]float64, len(ys))
		for i := range xs {
			xs[i] = float64(i)
		}
		xys := hplot.ZipXY(xs, ys)
		line, err := hplot.NewLine(xys)
		if err != nil {
			return nil, fmt.Errorf("could not create line plot for %q: %w", name, err)
		}
		line.Color = plotutil.SoftColors[i]
		line.Width = 2
		p.Add(line)
		p.Legend.Add(fmt.Sprintf("%s %8d", name, int(ys[len(ys)-1])), line)
		if lockdown, ok := lockDB[name]; ok {
			v := ds.cutoff[name]
			start := ds.start
			loc := start.Location()
			beg := time.Date(start.Year(), start.Month(), start.Day()+v, 0, 0, 0, 0, loc)
			lx := lockdown.Sub(beg).Hours() / 24
			vline := hplot.VLine(lx, nil, nil)
			vline.Line.Color = line.Color
			vline.Line.Dashes = plotutil.Dashes(1)
			vline.Line.Width = 2
			p.Add(vline)
			legends[name] = vline
		}
	}
	fct := hplot.NewFunction(func(x float64) float64 {
		return cutoff * math.Pow(1.33, x)
	})
	fct.LineStyle.Color = color.Gray16{}
	fct.LineStyle.Width = 2
	fct.LineStyle.Dashes = plotutil.Dashes(1)
	p.Add(fct)
	p.Legend.Add("33% daily growth", fct)
	for _, name := range []string{"Italy", "France"} {
		p.Legend.Add(fmt.Sprintf("%s - lockdown", name), legends[name])
	}
	p.Add(hplot.NewGrid())

	const sz = 20 * vg.Centimeter
	cnv := vgimg.PngCanvas{vgimg.New(sz*math.Phi, sz)}

	c := draw.New(cnv)
	p.Draw(c)
	return cnv.Image(), nil
}

type Dataset struct {
	date   time.Time
	start  time.Time
	table  map[string][]float64
	cutoff map[string]int
}

func fetchData(title string, cutoff float64, countries []string) (Dataset, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/CSSEGISandData/COVID-19/master/csse_covid_19_data/csse_covid_19_time_series/time_series_covid19_%s_global.csv", title)

	var dataset = Dataset{
		table:  make(map[string][]float64, len(countries)),
		cutoff: make(map[string]int, len(countries)),
	}

	resp, err := http.Get(url)
	if err != nil {
		return dataset, fmt.Errorf("could not retrieve data file: %w", err)
	}
	defer resp.Body.Close()

	raw := csv.NewReader(resp.Body)
	raw.Comma = ','

	hdr, err := raw.Read()
	if err != nil {
		return dataset, fmt.Errorf("could not read CSV header: %w", err)
	}

	sz := len(hdr) - 4
	for _, name := range countries {
		dataset.table[name] = make([]float64, sz)
	}

loop:
	for {
		rec, err := raw.Read()
		if err != nil {
			if err == io.EOF {
				break loop
			}
			return dataset, fmt.Errorf("could not read CSV data: %w", err)
		}

		if _, ok := dataset.table[rec[1]]; !ok {
			continue
		}

		name := rec[1]
		rec = rec[4:]
		data := make([]float64, len(rec))
		for i, str := range rec {
			if str == "" {
				continue
			}
			v, err := strconv.ParseFloat(str, 64)
			if err != nil {
				return dataset, fmt.Errorf("could not parse %q: %w", str, err)
			}
			data[i] = v
		}
		floats.Add(dataset.table[name], data)
	}

	for _, name := range countries {
		data := dataset.table[name]
		idx := 0
	cleanup:
		for i, v := range data {
			if v >= cutoff {
				idx = i
				dataset.cutoff[name] = idx
				break cleanup
			}
		}
		dataset.table[name] = data[idx:]
	}

	const layout = "1/2/06"
	for _, v := range []struct {
		input  string
		output *time.Time
	}{
		{hdr[4], &dataset.start},
		{hdr[len(hdr)-1], &dataset.date},
	} {
		date, err := time.Parse(layout, v.input)
		if err != nil {
			return dataset, fmt.Errorf("could not parse date: %w", err)
		}
		*v.output = date
	}

	cleanup(title, &dataset)

	return dataset, nil
}

func cleanup(title string, ds *Dataset) {
	switch title {
	case "Deaths":
		tbl := ds.table["France"]
		tbl[2] = 30   // 2020-03-09
		tbl[10] = 175 // 2020-03-17
		tbl[11] = 244 // 2020-03-18
		tbl[12] = 372 // 2020-03-19
	case "Confirmed":
	}
}

var (
	lockDB = map[string]time.Time{
		"Italy":  time.Date(2020, 2, 27, 0, 0, 0, 0, time.UTC), // lockdown of northern regions
		"France": time.Date(2020, 3, 17, 0, 0, 0, 0, time.UTC),
	}
)

const page = `<!DOCTYPE html>
<html>
	<head>
		<title>COVID-19</title>
	</head>
	<body>
		<div id="content">
			<img id="plot" src="/img-confirmed"/>
			<img id="plot" src="/img-deaths"/>
		</div>
	</body>
</html>
`
