package design

import (
	"bytes"
	"encoding/json"
	"html"
)

// ArtifactTitle is the artifact title of a design page. serve finds the
// artifact of an earlier revision by it, so one key keeps one URL.
func ArtifactTitle(repoName, key string) string {
	return "Design: " + repoName + "/" + key
}

// ArtifactPage wraps a design page's markdown in an HTML page that uses the
// artifact kit (kit/theme.css, kit/kit.js). The markdown is inlined as JSON
// and rendered in the browser by marked, because a sandboxed artifact cannot
// fetch its own files. kit.js then draws Mermaid blocks, legends and tables.
func ArtifactPage(md []byte, title string) []byte {
	data, _ := json.Marshal(string(md))
	// "<" inside the JSON must not close the script element.
	data = bytes.ReplaceAll(data, []byte("<"), []byte(`<`))
	var b bytes.Buffer
	b.WriteString(`<!doctype html>
<html lang="en" data-kit-auto="off">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`</title>
<link rel="stylesheet" href="kit/theme.css">
</head>
<body>
<main class="kit-page" id="doc"><p class="meta">Loading…</p></main>
<script id="md" type="application/json">`)
	b.Write(data)
	b.WriteString(`</script>
<script src="https://cdn.jsdelivr.net/npm/marked@15.0.12/marked.min.js"></script>
<script src="kit/kit.js"></script>
<script>
(function () {
  var doc = document.getElementById("doc");
  doc.innerHTML = marked.parse(JSON.parse(document.getElementById("md").textContent), { gfm: true });
  // The lines between the title and the first section are the page's meta.
  var h1 = doc.querySelector("h1");
  if (h1) {
    var eb = document.createElement("div");
    eb.className = "eyebrow";
    eb.textContent = "aiman · design";
    h1.before(eb);
    for (var n = h1.nextElementSibling; n && n.tagName !== "H2"; n = n.nextElementSibling) {
      if (n.tagName === "P") n.classList.add(n.querySelector("strong") ? "meta" : "lede");
    }
  }
  window.aimanKit.init(doc);
})();
</script>
</body>
</html>
`)
	return b.Bytes()
}
