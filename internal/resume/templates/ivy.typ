// Ivy: the classic one-column LaTeX resume ("Jake's resume") that tech
// recruiters know on sight. New Computer Modern, small-caps section
// titles over a rule, two-column entry rows, compact bullets.
#import "common.typ": *

#let small = fs * 10 / 11
#let large = fs * 12 / 11
#let top = margin * 0.8
#let bottom = margin * 0.8

#set document(title: data.name + " - Resume", author: data.name)
#set page(paper: paper, margin: (x: margin, top: top, bottom: bottom))
#set text(font: "New Computer Modern", size: fs, lang: "en", hyphenate: false)
#set par(leading: 0.5em, spacing: 0.5em, justify: false)
#set list(indent: 0.1in, body-indent: 0.45em, spacing: 0.34em * sp, marker: text(size: small * 0.85, baseline: -0.05em)[•])
#show link: it => it.body

// Header
#align(center, {
  text(size: fs * 2.2, smallcaps(data.name))
  if present(headline) {
    v(0.1em)
    block(text(size: fs, headline))
  }
  v(0.25em * sp)
  text(size: small, joined(contact-items(), [#h(0.4em)|#h(0.4em)]))
})

#let section-title(title) = block(above: 0.95em * sp + 0.25em, below: 0.5em * sp + 0.1em, sticky: true, width: 100%, {
  text(size: large, bottom-edge: "descender", smallcaps(title))
  v(0.12em)
  line(length: 100%, stroke: 0.45pt)
})

#let head-rows(l1, r1, l2, r2) = grid(
  columns: (1fr, auto),
  column-gutter: 1em,
  row-gutter: 0.42em,
  l1, align(right, r1),
  ..if l2 != none or r2 != none { (l2, align(right, r2)) } else { () },
)

#let bullet-list(e) = {
  let bs = bullets-of(e)
  if bs.len() > 0 {
    v(0.18em * sp + 0.1em)
    set text(size: small)
    list(..bs.map(b => rich(b.text)))
  }
}

#let entry-block(body) = block(above: 0.62em * sp + 0.12em, below: 0pt, breakable: true, pad(left: 0.12in, body))

#let entries(s) = for e in entries-of(s) {
  entry-block({
    let sub = field(e, "subheading")
    let loc = field(e, "location")
    head-rows(
      strong(maybe-link(field(e, "url"), field(e, "heading"))), field(e, "dates"),
      if present(sub) { text(size: small, emph(sub)) } else { none },
      if present(loc) { text(size: small, emph(loc)) } else { none },
    )
    bullet-list(e)
  })
}

#let projects(s) = for e in entries-of(s) {
  entry-block({
    let tech = field(e, "tech")
    let lead = if present(tech) [#strong(field(e, "heading")) #h(0.3em)|#h(0.3em) #text(size: small, emph(tech))] else { strong(field(e, "heading")) }
    head-rows(lead, text(size: small, field(e, "dates")), none, none)
    let desc = field(e, "text")
    if present(desc) or present(field(e, "url")) {
      v(0.3em)
      text(size: small, {
        rich(desc)
        if present(field(e, "url")) {
          let label = field(e, "link_label")
          [ #link(e.url, underline(if present(label) { label } else { "link" }))]
        }
      })
    }
    bullet-list(e)
  })
}

// One line each: "Title | Organization" and dates; a lone title (an
// award, a talk) is set in regular weight like a list line.
#let compact(s) = for e in entries-of(s) {
  let sub = field(e, "subheading")
  block(above: 0.42em * sp + 0.1em, below: 0pt, pad(left: 0.12in, head-rows(
    if present(sub) [#strong(field(e, "heading")) #h(0.3em)|#h(0.3em) #text(size: small, emph(sub))] else { rich(field(e, "heading")) },
    text(size: small, emph(field(e, "dates"))), none, none,
  )))
}

#let skills(s) = pad(left: 0.12in, text(size: small, {
  set par(leading: 0.42em)
  for (i, g) in s.at("skills", default: ()).enumerate() {
    if i > 0 { linebreak() }
    [#if g.name != "" [#strong(g.name + ":") ]#g.items]
  }
}))

#for s in sections {
  section-title(s.title)
  if s.kind == "entries" { entries(s) }
  else if s.kind == "projects" { projects(s) }
  else if s.kind == "compact" { compact(s) }
  else if s.kind == "skills" { skills(s) }
  else if s.kind == "summary" { pad(left: 0.12in, text(size: small, rich(s.at("text", default: "")))) }
}

#end-marker(top, bottom)
