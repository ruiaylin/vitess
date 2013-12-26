// Copyright 2013, Google Inc. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"html/template"
	"net/http"
	"sync"

	log "github.com/golang/glog"
	"github.com/youtube/vitess/go/vt/concurrency"
	"github.com/youtube/vitess/go/vt/wrangler"
)

const indexHTML = `
<!DOCTYPE html>
<head>
  <title>Worker Action Index</title>
</head>
<body>
  <h1>Worker Action Index</h1>
    {{range $i, $group := . }}
      <li><a href="/{{$group.Name}}">{{$group.Name}}</a>: {{$group.Description}}.</li>
    {{end}}
</body>
`

const subIndexHTML = `
{{$name := .Name}}
<!DOCTYPE html>
<head>
  <title>{{$name}} Index</title>
</head>
<body>
  <h1>{{$name}} Index</h1>
    <p>{{.Description}}</p>
    {{range $i, $cmd := .Commands }}
      <li><a href="{{$name}}/{{$cmd.Name}}">{{$cmd.Name}}</a>: {{$cmd.Help}}.</li>
    {{end}}
</body>
`

func httpError(w http.ResponseWriter, format string, err error) {
	log.Errorf(format, err)
	http.Error(w, fmt.Sprintf(format, err), http.StatusInternalServerError)
}

func loadTemplate(name, contents string) *template.Template {
	t, err := template.New(name).Parse(contents)
	if err != nil {
		log.Fatalf("Cannot parse %v template: %v", name, err)
	}
	return t
}

func executeTemplate(w http.ResponseWriter, t *template.Template, data interface{}) {
	if err := t.Execute(w, data); err != nil {
		httpError(w, "error executing template", err)
	}
}

// shardsWithSources returns all the shards that have SourceShards set.
func shardsWithSources(wr *wrangler.Wrangler) ([]map[string]string, error) {
	keyspaces, err := wr.TopoServer().GetKeyspaces()
	if err != nil {
		return nil, err
	}

	wg := sync.WaitGroup{}
	mu := sync.Mutex{} // protects result
	result := make([]map[string]string, 0, len(keyspaces))
	rec := concurrency.AllErrorRecorder{}
	for _, keyspace := range keyspaces {
		wg.Add(1)
		go func(keyspace string) {
			defer wg.Done()
			shards, err := wr.TopoServer().GetShardNames(keyspace)
			if err != nil {
				rec.RecordError(err)
				return
			}
			for _, shard := range shards {
				wg.Add(1)
				go func(keyspace, shard string) {
					defer wg.Done()
					si, err := wr.TopoServer().GetShard(keyspace, shard)
					if err != nil {
						rec.RecordError(err)
						return
					}

					if len(si.SourceShards) > 0 {
						mu.Lock()
						result = append(result, map[string]string{
							"Keyspace": keyspace,
							"Shard":    shard,
						})
						mu.Unlock()
					}
				}(keyspace, shard)
			}
		}(keyspace)
	}
	wg.Wait()

	if rec.HasErrors() {
		return nil, rec.Error()
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("There are no shards with SourceShards")
	}
	return result, nil
}

func initInteractiveMode(wr *wrangler.Wrangler) {
	indexTemplate := loadTemplate("index", indexHTML)
	subIndexTemplate := loadTemplate("subIndex", subIndexHTML)

	// toplevel menu
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		executeTemplate(w, indexTemplate, commands)
	})

	// command group menus
	for _, cg := range commands {
		http.HandleFunc("/"+cg.Name, func(w http.ResponseWriter, r *http.Request) {
			executeTemplate(w, subIndexTemplate, cg)
		})

		for _, c := range cg.Commands {
			http.HandleFunc("/"+cg.Name+"/"+c.Name, func(w http.ResponseWriter, r *http.Request) {
				c.interactive(wr, w, r)
			})
		}
	}

	log.Infof("Interactive mode ready")
}
