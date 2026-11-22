// Parse: built to be read by applicant tracking systems first. One
// column, a plain sans font, standard uppercase section names, no small
// caps or ligatures, every entry on lines a parser reads in order
// ("Software Engineer, Stripe" then the dates), links spelled out.
#import "common.typ": *

#let small = fs * 0.95
#let top = margin * 0.85
#let bottom = margin * 0.8

#set document(title: data.name + " - Resume", author: data.name)
#set page(paper: paper, margin: (x: margin, top: top, bottom: bottom))
#set text(font: "Source Sans 3", size: fs, lang: "en", hyphenate: false, ligatures: false, discretionary-ligatures: false)
#set par(leading: 0.52em, spacing: 0.5em)
#set list(indent: 0.05in, body-indent: 0.5em, spacing: 0.4em * sp, marker: [•])
#show link: it => it.body
#set strong(delta: 300)

// Header: name, optional headline, contact line.
#block(below: 0.2em, text(size: fs * 2, weight: "bold", data.name))
#if present(headline) { block(above: 0.35em, below: 0.2em, text(size: fs * 1.1, headline)) }
#block(above: 0.45em, text(size: small, joined(contact-items(), [ #h(0.2em)|#h(0.2em) ])))

#let section-title(title) = block(above: 1.0em * sp + 0.3em, below: 0.55em * sp + 0.1em, sticky: true, width: 100%, {
  text(size: fs * 1.05, weight: "bold", tracking: 0.04em, bottom-edge: "descender", upper(title))
  v(0.1em)
  line(length: 100%, stroke: 0.6pt)
})

#let head-line(lhs, rhs) = grid(columns: (1fr, auto), column-gutter: 1em, lhs, align(right, rhs))

#let bullet-list(e) = {
  let bs = bullets-of(e)
  if bs.len() > 0 {
    v(0.25em * sp + 0.1em)
    list(..bs.map(b => rich(b.text)))
  }
}

#let entry-block(body) = block(above: 0.75em * sp + 0.15em, below: 0pt, breakable: true, body)

// "Title, Organization" when there is a title (a job, a degree), else the
// organization alone; location after a separator.
#let role-line(e) = {
  let heading = field(e, "heading")
  let sub = field(e, "subheading")
  let lead = if present(sub) [#strong(sub), #heading] else { strong(heading) }
  joined((lead, field(e, "location")), [ #h(0.15em)|#h(0.15em) ])
}

#let entries(s) = for e in entries-of(s) {
  entry-block({
    head-line(role-line(e), field(e, "dates"))
    bullet-list(e)
  })
}

#let projects(s) = for e in entries-of(s) {
  entry-block({
    let tech = field(e, "tech")
    head-line(joined((strong(field(e, "heading")), tech), [ #h(0.15em)|#h(0.15em) ]), field(e, "dates"))
    let desc = field(e, "text")
    let url = field(e, "url")
    if present(desc) or present(url) {
      v(0.3em)
      rich(desc)
      if present(url) { [ #link(url, url.replace("https://", "").replace("http://", ""))] }
    }
    bullet-list(e)
  })
}

#let compact(s) = for e in entries-of(s) {
  let sub = field(e, "subheading")
  block(above: 0.45em * sp + 0.1em, below: 0pt, head-line(
    if present(sub) [#strong(field(e, "heading")), #sub] else { rich(field(e, "heading")) },
    field(e, "dates"),
  ))
}

#let skills(s) = {
  set par(leading: 0.5em)
  for (i, g) in s.at("skills", default: ()).enumerate() {
    if i > 0 { linebreak() }
    [#if g.name != "" [#strong(g.name + ":") ]#g.items]
  }
}

#for s in sections {
  section-title(s.title)
  if s.kind == "entries" { entries(s) }
  else if s.kind == "projects" { projects(s) }
  else if s.kind == "compact" { compact(s) }
  else if s.kind == "skills" { skills(s) }
  else if s.kind == "summary" { rich(s.at("text", default: "")) }
}

#end-marker(top, bottom)
