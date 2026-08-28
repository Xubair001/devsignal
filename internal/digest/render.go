package digest

import (
	"fmt"
	"html"
	"strings"

	"github.com/Xubair001/devsignal/internal/opportunity"
	"github.com/Xubair001/devsignal/internal/store"
)

// Render turns a result into a message.
//
// The display rules apply here exactly as they do in the console, and email is
// the harder case: nobody can click through to a caveat, and a screenshot of a
// digest outlives any correction. So the same three things hold — no percentage,
// no invented salary, and no role whose open state is unknown — plus one more
// that is specific to email: the per-factor arithmetic travels WITH the item,
// because the reader cannot expand a card to ask why.
func Render(u store.DigestCandidateUsersRow, res Result) Message {
	m := Message{UserID: res.UserID, Subject: subject(res)}
	m.Text = renderText(res)
	m.HTML = renderHTML(res)
	return m
}

// EmptySubject is the subject line of a digest with nothing in it.
//
// A named constant because it is asserted in tests and appears in both the text
// and HTML bodies: an empty digest that says "Your daily digest" is the small
// dishonesty that teaches someone to stop opening the channel.
const EmptySubject = "Nothing met your bar today"

func subject(res Result) string {
	switch {
	case len(res.Items) == 0:
		// Says what happened. "Your daily digest" over an empty digest is the
		// small dishonesty that teaches people to stop opening it.
		return EmptySubject
	case len(res.Items) == 1:
		return "1 role worth your time: " + res.Items[0].Posting.Title
	default:
		return fmt.Sprintf("%d roles worth your time", len(res.Items))
	}
}

func renderText(res Result) string {
	var b strings.Builder

	if len(res.Items) == 0 {
		b.WriteString(EmptySubject + ".\n\n")
		b.WriteString(wrap(res.Reason, 72))
		b.WriteString("\n\nThis is a real result, not a quiet failure. We do not pad the\n")
		b.WriteString("list to hit a number: a role we cannot honestly recommend costs\n")
		b.WriteString("you more to read than it costs us to leave out.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "%d roles cleared your bar today.\n\n", len(res.Items))

	for i, it := range res.Items {
		p := it.Posting
		fmt.Fprintf(&b, "%d. %s\n", i+1, p.Title)
		fmt.Fprintf(&b, "   %s", p.Company.Name)
		if !p.Company.DomainConfirmed {
			b.WriteString(" (company identified from its job-board token)")
		}
		if where := location(p); where != "" {
			fmt.Fprintf(&b, " — %s", where)
		}
		b.WriteString("\n")

		// Liveness. The product's central claim, and in email it has to carry its
		// own timestamp because the reader may open this days later.
		if p.Liveness.CheckedAt != nil {
			fmt.Fprintf(&b, "   %s, last checked %s\n",
				openState(p), p.Liveness.CheckedAt.Format("2 Jan 15:04 MST"))
		} else {
			fmt.Fprintf(&b, "   %s\n", openState(p))
		}

		// Salary. `nil` is a state.
		if p.Salary != nil {
			fmt.Fprintf(&b, "   %s", money(*p.Salary))
			if p.Salary.IsEstimated {
				b.WriteString(" (our estimate, not the employer's)")
			}
			b.WriteString("\n")
		} else {
			b.WriteString("   Salary not disclosed\n")
		}

		// The band and the arithmetic behind it. No percentage.
		fit := it.Match.Fit
		fmt.Fprintf(&b, "   %s — %s\n", fit.Band(), fit.Summary())
		for _, f := range fit.Factors {
			if !f.Available {
				continue
			}
			fmt.Fprintf(&b, "     + %.4g of %.4g  %s\n",
				f.Contribution, f.MaxContribution, factorLabel(f.Factor))
		}
		// Unscored factors are named, not hidden. A reader who cannot see that
		// half the model was unavailable will read the band as more confident
		// than it is.
		var missing []string
		for _, f := range fit.Factors {
			if !f.Available {
				missing = append(missing, factorLabel(f.Factor))
			}
		}
		if len(missing) > 0 {
			fmt.Fprintf(&b, "     not scored: %s\n", strings.Join(missing, ", "))
		}

		if p.ApplyURL != nil {
			fmt.Fprintf(&b, "   %s\n", *p.ApplyURL)
		} else {
			b.WriteString("   No application link was published for this role\n")
		}
		b.WriteString("\n")
	}

	if res.AlreadySent > 0 {
		fmt.Fprintf(&b, "%d other roles cleared your bar but were in a recent digest.\n",
			res.AlreadySent)
	}
	return b.String()
}

func renderHTML(res Result) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:ui-sans-serif,system-ui,sans-serif;` +
		`max-width:600px;color:#0F172A">`)

	if len(res.Items) == 0 {
		fmt.Fprintf(&b, `<h1 style="font-size:18px">%s.</h1>`,
			html.EscapeString(EmptySubject))
		fmt.Fprintf(&b, `<p style="font-size:14px;line-height:1.6">%s</p>`,
			html.EscapeString(res.Reason))
		b.WriteString(`<p style="font-size:13px;color:#64748B;line-height:1.6">` +
			`This is a real result, not a quiet failure. We do not pad the list to ` +
			`hit a number.</p></div>`)
		return b.String()
	}

	fmt.Fprintf(&b, `<h1 style="font-size:18px">%d roles cleared your bar today.</h1>`,
		len(res.Items))

	for _, it := range res.Items {
		p := it.Posting
		fit := it.Match.Fit
		b.WriteString(`<div style="border:1px solid #E2E8F0;border-radius:12px;` +
			`padding:16px;margin:12px 0">`)
		fmt.Fprintf(&b, `<div style="font-weight:600;font-size:15px">%s</div>`,
			html.EscapeString(p.Title))
		fmt.Fprintf(&b, `<div style="font-size:13px;color:#475569;margin-top:2px">%s`,
			html.EscapeString(p.Company.Name))
		if where := location(p); where != "" {
			fmt.Fprintf(&b, ` — %s`, html.EscapeString(where))
		}
		b.WriteString(`</div>`)

		fmt.Fprintf(&b,
			`<div style="font-size:12px;color:%s;margin-top:6px;font-weight:600">%s</div>`,
			livenessColour(p), html.EscapeString(openState(p)))

		if p.Salary != nil {
			fmt.Fprintf(&b, `<div style="font-size:13px;margin-top:6px">%s</div>`,
				html.EscapeString(money(*p.Salary)))
		} else {
			b.WriteString(`<div style="font-size:13px;color:#64748B;margin-top:6px">` +
				`Salary not disclosed</div>`)
		}

		// The ledger, as a table. Not a bar and not a ring: a proportional graphic
		// IS a percentage, and email clients strip the markup that would label it.
		fmt.Fprintf(&b,
			`<div style="font-size:13px;font-weight:600;margin-top:10px">%s</div>`+
				`<div style="font-size:12px;color:#64748B">%s</div>`,
			html.EscapeString(string(fit.Band())), html.EscapeString(fit.Summary()))
		b.WriteString(`<table style="font-size:12px;margin-top:6px;` +
			`border-collapse:collapse" role="presentation">`)
		for _, f := range fit.Factors {
			label := html.EscapeString(factorLabel(f.Factor))
			if f.Available {
				fmt.Fprintf(&b,
					`<tr><td style="padding:1px 8px 1px 0">+%.4g of %.4g</td>`+
						`<td>%s</td></tr>`, f.Contribution, f.MaxContribution, label)
			} else {
				fmt.Fprintf(&b,
					`<tr><td style="padding:1px 8px 1px 0;color:#94A3B8">not scored</td>`+
						`<td style="color:#94A3B8"><i>%s</i></td></tr>`, label)
			}
		}
		b.WriteString(`</table>`)

		if p.ApplyURL != nil {
			fmt.Fprintf(&b,
				`<div style="margin-top:12px"><a href="%s" `+
					`style="font-size:13px;font-weight:600;color:#0B6FA4">Open role</a></div>`,
				html.EscapeString(*p.ApplyURL))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func openState(p opportunity.Summary) string {
	if p.Liveness.VerifiedOpen {
		return "Verified open"
	}
	return "Not verified recently"
}

func livenessColour(p opportunity.Summary) string {
	if p.Liveness.VerifiedOpen {
		return "#15803D"
	}
	return "#B45309"
}

func location(p opportunity.Summary) string {
	var parts []string
	if p.Location.WorkMode != nil {
		parts = append(parts, strings.ToUpper((*p.Location.WorkMode)[:1])+
			(*p.Location.WorkMode)[1:])
	}
	if p.Location.City != nil {
		parts = append(parts, *p.Location.City)
	}
	if p.Location.Country != nil {
		parts = append(parts, *p.Location.Country)
	}
	// The geo scope is the eligible hiring region and only means something for a
	// remote role; on an onsite one it would read as a list of offices.
	if p.Location.WorkMode != nil && *p.Location.WorkMode == "remote" &&
		len(p.Location.GeoScope) > 0 {
		parts = append(parts, "("+strings.Join(p.Location.GeoScope, ", ")+")")
	}
	return strings.Join(parts, " · ")
}

// money formats from minor units at the render edge. Never stored formatted.
func money(m opportunity.Money) string {
	unit := func(v int64) string {
		major := v / 100
		if major >= 1000 {
			return fmt.Sprintf("%s%d,%03d", m.Currency+" ", major/1000, major%1000)
		}
		return fmt.Sprintf("%s%d", m.Currency+" ", major)
	}
	per := m.Period
	if m.MaxMinor != nil && *m.MaxMinor != m.MinMinor {
		return fmt.Sprintf("%s–%s per %s", unit(m.MinMinor), unit(*m.MaxMinor), per)
	}
	return fmt.Sprintf("%s per %s", unit(m.MinMinor), per)
}

func factorLabel(f string) string {
	switch f {
	case "required_skills":
		return "required skills"
	case "preferred_skills":
		return "preferred skills"
	case "semantic":
		return "overall role similarity"
	case "seniority":
		return "seniority"
	case "domain":
		return "domain"
	case "compensation":
		return "compensation"
	default:
		return f
	}
}

// wrap breaks text at a width, for the plain-text part.
func wrap(s string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			b.WriteString(line + "\n")
			line = w
			continue
		}
		line += " " + w
	}
	b.WriteString(line)
	return b.String()
}
