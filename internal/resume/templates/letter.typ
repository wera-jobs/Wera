// A cover letter, for application forms that want it as a file. Like the
// resume templates, every word comes from letter.json as a string, so
// nothing the user or the AI wrote can run as Typst code.

#let d = json("letter.json")
#let present(v) = v != none and v != ""

#set document(title: d.at("name", default: "") + " cover letter")
#set page(paper: d.at("paper", default: "us-letter"), margin: 1in)
#set text(font: "Source Sans 3", size: 11pt, fill: rgb("#1d1f24"))
#set par(justify: false, leading: 0.68em, spacing: 1.05em)

#text(size: 17pt, weight: "semibold", d.name)
#v(-0.4em)
#text(size: 9.5pt, fill: rgb("#5b6170"),
  (d.at("email", default: ""), d.at("phone", default: ""), d.at("location", default: ""))
    .filter(present).join("  ·  ", default: ""))
#line(length: 100%, stroke: 0.6pt + rgb("#d5d8de"))
#v(0.6em)

#d.date
#if present(d.at("company", default: "")) [
  #v(0.2em)
  #d.company
  #if present(d.at("title", default: "")) [ \ #text(fill: rgb("#5b6170"), "Re: " + d.title) ]
]
#v(0.8em)

// Blank lines part paragraphs; a single line break stays one, as on screen
// (a sign-off and the name under it).
#for p in d.body.replace("\r\n", "\n").split("\n\n").map(p => p.trim()).filter(present) [
  #par(p.split("\n").map(l => l.trim()).join(linebreak()))
]
