// Signal: a modern, confident layout. Inter, a left-aligned header with
// the headline in the accent color, section titles in small accented caps
// with a hairline that runs to the margin, muted dates and places, and
// skills as a two-column grid.
#import "common.typ": *

// Inter runs large: 10.5pt here sets like 11pt in a serif.
#let base = fs * 0.93
#let small = base * 0.94
#let ink = rgb("#16181d")
#let muted = rgb("#5b616e")
#let top = margin * 0.85
#let bottom = margin * 0.8

#set document(title: data.name + " - Resume", author: data.name)
#set page(paper: paper, margin: (x: margin, top: top, bottom: bottom))
#set text(font: "Inter", size: base, fill: ink, lang: "en", hyphenate: false, number-type: "lining")
#set par(leading: 0.55em, spacing: 0.5em)
#set strong(delta: 200)
#set list(indent: 0.02in, body-indent: 0.55em, spacing: 0.42em * sp, marker: text(fill: accent, weight: "bold")[•])
#show link: it => it.body

// Header
#block(below: 0pt, text(size: base * 2.25, weight: "semibold", tracking: -0.015em, data.name))
#if present(headline) { block(above: 0.5em, below: 0pt, text(size: base * 1.12, weight: "medium", fill: accent, headline)) }
#block(above: 0.7em, below: 0pt, text(size: small, fill: muted, joined(contact-items(), [#h(0.45em)·#h(0.45em)])))

#let section-title(title) = block(above: 1.15em * sp + 0.35em, below: 0.6em * sp + 0.15em, sticky: true, width: 100%,
  grid(columns: (auto, 1fr), column-gutter: 0.6em, align: horizon,
    text(size: small, weight: "semibold", fill: accent, tracking: 0.09em, upper(title)),
    line(length: 100%, stroke: 0.5pt + accent.lighten(55%)),
  ))

#let head-rows(l1, r1, l2, r2) = grid(
  columns: (1fr, auto),
  column-gutter: 1em,
  row-gutter: 0.45em,
  l1, align(right, text(size: small, fill: muted, r1)),
  ..if l2 != none or r2 != none { (l2, align(right, text(size: small, fill: muted, r2))) } else { () },
)

#let bullet-list(e) = {
  let bs = bullets-of(e)
  if bs.len() > 0 {
    v(0.3em * sp + 0.1em)
    list(..bs.map(b => rich(b.text)))
  }
}

#let entry-block(body) = block(above: 0.85em * sp + 0.2em, below: 0pt, breakable: true, body)

#let entries(s) = for e in entries-of(s) {
  entry-block({
    let sub = field(e, "subheading")
    let loc = field(e, "location")
    head-rows(
      text(weight: "semibold", maybe-link(field(e, "url"), field(e, "heading"))), field(e, "dates"),
      if present(sub) { text(weight: "medium", fill: ink.lighten(15%), sub) } else { none },
      if present(loc) { loc } else { none },
    )
    bullet-list(e)
  })
}

#let projects(s) = for e in entries-of(s) {
  entry-block({
    let tech = field(e, "tech")
    head-rows(
      [#text(weight: "semibold", field(e, "heading"))#if present(tech) [#h(0.5em)#text(size: small, fill: muted, tech)]],
      field(e, "dates"), none, none,
    )
    let desc = field(e, "text")
    if present(desc) or present(field(e, "url")) {
      v(0.35em)
      rich(desc)
      if present(field(e, "url")) {
        let label = field(e, "link_label")
        [ #link(e.url, text(fill: accent, if present(label) { label } else { "link" }))]
      }
    }
    bullet-list(e)
  })
}

#let compact(s) = for e in entries-of(s) {
  let sub = field(e, "subheading")
  block(above: 0.5em * sp + 0.12em, below: 0pt, head-rows(
    if present(sub) [#text(weight: "semibold", field(e, "heading"))#h(0.5em)#text(fill: muted, sub)] else { rich(field(e, "heading")) },
    field(e, "dates"), none, none,
  ))
}

#let skills(s) = grid(
  columns: (auto, 1fr),
  column-gutter: 1.1em,
  row-gutter: 0.55em,
  ..s.at("skills", default: ()).map(g => if g.name == "" { (grid.cell(colspan: 2, g.items),) } else { (text(weight: "semibold", g.name), g.items) }).flatten(),
)

#for s in sections {
  section-title(s.title)
  if s.kind == "entries" { entries(s) }
  else if s.kind == "projects" { projects(s) }
  else if s.kind == "compact" { compact(s) }
  else if s.kind == "skills" { skills(s) }
  else if s.kind == "summary" { rich(s.at("text", default: "")) }
}

#end-marker(top, bottom)
