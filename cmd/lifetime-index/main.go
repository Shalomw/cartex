// Command lifetime-index 对目标源码运行启发式 lifetime 索引器，输出
// IndexOutput JSON 并打印摘要。用于 bootstrap 前的本地验证与调试。
//
// 用法：
//
//	lifetime-index -root /src/v8 \
//	  -file src/objects/js-number-format.cc \
//	  -subdir src/objects \
//	  -max-files 400 \
//	  -out /tmp/index.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/Autumn-27/artex/lifetime"
	"github.com/Autumn-27/artex/lifetime/index"
)

func main() {
	var (
		root    = flag.String("root", "", "source root (e.g. /src/v8)")
		files   = flag.String("file", "", "comma-separated target files relative to root")
		subdirs = flag.String("subdir", "", "comma-separated target subdirs relative to root")
		max     = flag.Int("max-files", 400, "max files to scan (0 = default)")
		out     = flag.String("out", "", "write IndexOutput JSON to this path")
	)
	flag.Parse()
	if *root == "" {
		log.Fatal("lifetime-index: -root is required")
	}
	opts := index.Options{
		SourceRoot:    *root,
		TargetFiles:   splitList(*files),
		TargetSubdirs: splitList(*subdirs),
		MaxFiles:      *max,
	}
	if len(opts.TargetFiles) == 0 && len(opts.TargetSubdirs) == 0 {
		log.Fatal("lifetime-index: at least one -file or -subdir is required")
	}

	idx, err := index.Build(opts)
	if err != nil {
		log.Fatalf("lifetime-index: %v", err)
	}
	if *out != "" {
		b, err := json.MarshalIndent(idx, "", "  ")
		if err != nil {
			log.Fatalf("lifetime-index: marshal: %v", err)
		}
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			log.Fatalf("lifetime-index: write %s: %v", *out, err)
		}
	}

	fmt.Printf("index_hash=%s quality=%s type_resolution=%s\n",
		idx.IndexHash, idx.IndexQuality, idx.TypeResolution)
	fmt.Printf("families=%d domains=%d domain_pairs=%d lifetime_points=%d sync=%d callbacks=%d relations=%d unknowns=%d\n",
		len(idx.ObjectFamilies), len(idx.Domains), len(idx.DomainPairs),
		len(idx.LifetimePoints), len(idx.SyncPrimitives), len(idx.Callbacks),
		len(idx.ObjectRelations), len(idx.Unknowns))

	byForm := map[string]int{}
	for _, fam := range idx.ObjectFamilies {
		for _, f := range fam.ApplicableForms {
			byForm[string(f)]++
		}
	}
	forms := make([]string, 0, len(byForm))
	for f := range byForm {
		forms = append(forms, f)
	}
	sort.Strings(forms)
	var parts []string
	for _, f := range forms {
		parts = append(parts, fmt.Sprintf("%s=%d", f, byForm[f]))
	}
	fmt.Printf("forms: %s\n", strings.Join(parts, " "))

	fmt.Println("top families by applicable forms (first 20):")
	n := len(idx.ObjectFamilies)
	if n > 20 {
		n = 20
	}
	for _, fam := range idx.ObjectFamilies[:n] {
		fmt.Printf("  %-40s %-14s %-8s %-10s %s\n",
			truncate(fam.TypeCanonical, 40), fam.StorageClass, fam.LifetimeForm,
			fam.AllocSite, strings.Join(formStrings(fam.ApplicableForms), ","))
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func formStrings(in []lifetime.Form) []string {
	out := make([]string, 0, len(in))
	for _, f := range in {
		out = append(out, string(f))
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
