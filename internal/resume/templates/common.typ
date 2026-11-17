// Shared by every Wera resume template. All content comes from
// resume.json and layout.json as data, never as markup: text is only
// ever placed as strings, so nothing a user types can run as Typst code.

#let data = json("resume.json")
#let lay = json("layout.json")

// Layout, already clamped by the server.
#let fs = lay.font_size * 1pt
#let sp = lay.spacing
#let accent = rgb(lay.at("accent", default: "#1f3a8a"))
#let paper = lay.at("paper", default: "us-letter")
#let margin = lay.margin * 1in

// A field of an entry, "" when missing.
#let field(e, k) = {
  let v = e.at(k, default: "")
  if v == none { "" } else { v }
}
#let present(v) = v != none and v != ""
#let visible(items) = items.filter(x => not x.at("hidden", default: false))

// "**bold**" inside a line becomes bold; everything else stays text.
#let rich(s) = {
  let parts = s.split("**")
  if parts.len() < 3 { return s }
  for (i, p) in parts.enumerate() {
    if calc.odd(i) and i < parts.len() - 1 { strong(p) } else if calc.odd(i) { "**" + p } else { p }
  }
}

// Joins the non-empty parts with a separator.
#let joined(parts, sep) = {
  let kept = parts.filter(p => p != none and p != "" and p != [])
  if kept.len() == 0 { none } else { kept.join(sep) }
}

#let maybe-link(url, body) = if present(url) { link(url, body) } else { body }

// The header's contact items, in order, as content.
#let contact-items(plain-urls: false) = {
  let items = ()
  if present(data.at("phone", default: "")) { items.push(data.phone) }
  if present(data.at("email", default: "")) { items.push(link("mailto:" + data.email, data.email)) }
  if present(data.at("location", default: "")) { items.push(data.location) }
  // A link still being typed may have a label and no address yet: print the label.
  for l in data.at("links", default: ()) {
    let label = if present(l.label) { l.label } else { l.url }
    if present(label) { items.push(maybe-link(l.url, label)) }
  }
  items
}

#let headline = data.at("headline", default: "")
#let sections = visible(data.at("sections", default: ()))
#let entries-of(s) = visible(s.at("entries", default: ()))
#let bullets-of(e) = visible(e.at("bullets", default: ()))

// Where the content ends, for one-page fitting and the fill meter.
#let end-marker(top, bottom) = context {
  let pos = here().position()
  [#metadata((page: pos.page, y: pos.y.pt(), top: top.pt(), bottom: bottom.pt(), height: page.height.pt())) <wera-end>]
}
