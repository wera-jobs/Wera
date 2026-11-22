// Ledger: a refined serif layout for finance, consulting, law and
// research. Libertinus Serif, a centered letterspaced name, section
// titles centered between hairlines, quiet italics for titles and places.
#import "common.typ": *

#let small = fs * 0.93
#let top = margin * 0.85
#let bottom = margin * 0.8
#let rule-ink = luma(150)

#set document(title: data.name + " - Resume", author: data.name)
#set page(paper: paper, margin: (x: margin * 1.05, top: top, bottom: bottom))
#set text(font: "Libertinus Serif", size: fs * 1.02, lang: "en", hyphenate: false, number-type: "lining")
#set par(leading: 0.52em, spacing: 0.5em)
#set list(indent: 0.12in, body-indent: 0.5em, spacing: 0.36em * sp, marker: [–])
#show link: it => it.body

// Header
#align(center, {
  text(size: fs * 1.95, tracking: 0.14em, upper(data.name))
  if present(headline) {
    v(0.25em)
    block(text(size: fs * 1.02, style: "italic", headline))
  }
  v(0.35em * sp + 0.1em)
  text(size: small, number-type: "lining", joined(contact-items(), [#h(0.5em)·#h(0.5em)]))
})

#let section-title(title) = block(above: 1.05em * sp + 0.3em, below: 0.6em * sp + 0.15em, sticky: true, width: 100%,
  grid(columns: (1fr, auto, 1fr), column-gutter: 0.8em, align: horizon,
    line(length: 100%, stroke: 0.4pt + rule-ink),
    text(size: fs * 0.98, tracking: 0.16em, smallcaps(lower(title))),
    line(length: 100%, stroke: 0.4pt + rule-ink),
  ))

#let head-rows(l1, r1, l2, r2) = grid(
  columns: (1fr, auto),
  column-gutter: 1em,
  row-gutter: 0.4em,
  l1, align(right, r1),
  ..if l2 != none or r2 != none { (l2, align(right, r2)) } else { () },
)

#let bullet-list(e) = {
  let bs = bullets-of(e)
  if bs.len() > 0 {
    v(0.2em * sp + 0.12em)
    list(..bs.map(b => rich(b.text)))
  }
}

#let entry-block(body) = block(above: 0.75em * sp + 0.15em, below: 0pt, breakable: true, body)

#let entries(s) = for e in entries-of(s) {
  entry-block({
    let sub = field(e, "subheading")
    let loc = field(e, "location")
    head-rows(
      strong(maybe-link(field(e, "url"), field(e, "heading"))), if present(loc) { emph(loc) } else { "" },
      if present(sub) { emph(sub) } else { none }, field(e, "dates"),
    )
    bullet-list(e)
  })
}

#let projects(s) = for e in entries-of(s) {
  entry-block({
    let tech = field(e, "tech")
    head-rows(joined((strong(field(e, "heading")), if present(tech) { emph(tech) } else { "" }), [, ]), field(e, "dates"), none, none)
    let desc = field(e, "text")
    if present(desc) or present(field(e, "url")) {
      v(0.3em)
      rich(desc)
      if present(field(e, "url")) {
        let label = field(e, "link_label")
        [ #link(e.url, underline(if present(label) { label } else { "link" }))]
      }
    }
    bullet-list(e)
  })
}

#let compact(s) = for e in entries-of(s) {
  let sub = field(e, "subheading")
  block(above: 0.45em * sp + 0.1em, below: 0pt, head-rows(
    if present(sub) [#strong(field(e, "heading")), #emph(sub)] else { rich(field(e, "heading")) },
    field(e, "dates"), none, none,
  ))
}

#let skills(s) = {
  set par(leading: 0.48em)
  for (i, g) in s.at("skills", default: ()).enumerate() {
    if i > 0 { linebreak() }
    [#if g.name != "" [#smallcaps(lower(g.name)): ]#g.items]
  }
}

#for s in sections {
  section-title(s.title)
  if s.kind == "entries" { entries(s) }
  else if s.kind == "projects" { projects(s) }
  else if s.kind == "compact" { compact(s) }
  else if s.kind == "skills" { skills(s) }
  else if s.kind == "summary" {
    // A short summary sits centered under the header; a long one reads better left-aligned.
    let t = s.at("text", default: "")
    if t.len() <= 240 { align(center, block(width: 92%, text(style: "italic", rich(t)))) } else { rich(t) }
  }
}

#end-marker(top, bottom)
