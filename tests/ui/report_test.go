package ui_test

import (
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

type result struct {
	Name      string   `json:"name"`
	Failed    bool     `json:"failed"`
	Duration  string   `json:"duration"`
	Directory string   `json:"directory"`
	Files     []string `json:"files"`
}

type report struct {
	mu      sync.Mutex
	Results []result
}

func (r *report) add(value result) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.Results = append(r.Results, value)
}

func (r *report) write() error {
	slices.SortFunc(r.Results, func(a, b result) int { return strings.Compare(a.Name, b.Name) })

	for index := range r.Results {
		files, err := os.ReadDir(r.Results[index].Directory)
		if err != nil {
			return err
		}

		for _, file := range files {
			relative, err := filepath.Rel("artifacts", filepath.Join(r.Results[index].Directory, file.Name()))
			if err != nil {
				return err
			}

			r.Results[index].Files = append(r.Results[index].Files, filepath.ToSlash(relative))
		}
	}

	data, err := json.MarshalIndent(r.Results, "", "  ")
	if err != nil {
		return err
	}

	err = os.WriteFile("artifacts/results.json", data, 0o600)
	if err != nil {
		return err
	}

	file, err := os.Create("artifacts/index.html")
	if err != nil {
		return err
	}
	defer file.Close()

	return template.Must(template.New("report").Parse(reportHTML)).Execute(file, r.Results)
}

const reportHTML = `<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Padmark browser tests</title>
<style>
body{font:16px system-ui;max-width:1100px;margin:32px auto;padding:0 20px;color:#222}
article{padding:16px 0;border-top:1px solid #ddd}h2{font-size:16px}.pass{color:#16723b}.fail{color:#b42318}
img{display:block;max-width:100%;border:1px solid #ccc;margin:12px 0}li{margin:6px 0}
</style>
<h1>Padmark browser tests</h1>
<p>{{len .}} scenarios.
Failure artifacts include screenshots, pixel differences, server logs and Playwright traces.</p>
{{range .}}<article>
<h2 class="{{if .Failed}}fail{{else}}pass{{end}}">{{if .Failed}}FAIL{{else}}PASS{{end}} — {{.Name}}</h2>
<p>{{.Duration}}</p><ul>{{range .Files}}<li><a href="{{.}}">{{.}}</a></li>{{end}}</ul></article>{{end}}
</html>`
