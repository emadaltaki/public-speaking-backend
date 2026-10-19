package api

import (
	"bytes"
	"html/template"
	"strconv"
	"strings"

	"github.com/emadaltaki/public-speaking-backend/internal/mail"
)

// emailView fills the shared branded email layout.
type emailView struct {
	Lang, Dir   string
	Preheader   string
	Title       string
	Greeting    string
	Paragraphs  []string
	Code        string // big code block, optional
	CodeLabel   string
	ButtonLabel string
	ButtonURL   string
	Note        string // small print under the button
	SiteURL     string
	LogoURL     string
	Tagline     string
	UnsubLabel  string
	UnsubURL    string // empty for emails people can't opt out of (codes)
}

// emailLayout uses the site's colours (light and dark), table layout and
// inline styles so it renders the same in Gmail, Outlook and Apple Mail.
var emailLayout = template.Must(template.New("email").Parse(`<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="supported-color-schemes" content="light dark">
<title>{{.Title}}</title>
<style>
  body{margin:0;padding:0;background:#F1DDBC}
  a{color:#B4583A}
  @media (max-width:520px){.card{padding:28px 20px!important}.title{font-size:24px!important}.code{font-size:32px!important;letter-spacing:6px!important}}
  @media (prefers-color-scheme:dark){
    body,.bg{background:#17120F!important}
    .card{background:#221B16!important;border-color:#3A2F27!important}
    .text,.title{color:#F2E9DF!important}
    .muted{color:#B3A699!important}
    .codebox{background:#3A2720!important;color:#F2E9DF!important}
    .btn{background:#E08A68!important;color:#1A130F!important}
    a{color:#E08A68!important}
  }
</style>
</head>
<body style="margin:0;padding:0;background:#F1DDBC">
<div style="display:none;max-height:0;overflow:hidden;opacity:0">{{.Preheader}}</div>
<table role="presentation" class="bg" width="100%" cellpadding="0" cellspacing="0" style="background:#F1DDBC">
<tr><td align="center" style="padding:32px 12px">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:520px">
    <tr><td align="center" style="padding-bottom:20px">
      <a href="{{.SiteURL}}" style="text-decoration:none">
        <img src="{{.LogoURL}}" width="44" height="44" alt="" style="display:inline-block;vertical-align:middle;border-radius:12px;border:0">
        <span class="title" style="display:inline-block;vertical-align:middle;margin:0 10px;font-family:Montserrat,'Segoe UI',Arial,sans-serif;font-size:18px;font-weight:800;color:#2B2118">Speak Up Fearless</span>
      </a>
    </td></tr>
    <tr><td class="card" style="background:#FFFFFF;border:1px solid #E8E0D6;border-radius:24px;padding:36px 32px;font-family:Inter,'Segoe UI',Arial,sans-serif">
      <h1 class="title" style="margin:0 0 16px;font-family:Montserrat,'Segoe UI',Arial,sans-serif;font-size:28px;line-height:1.2;font-weight:800;color:#2B2118">{{.Title}}</h1>
      {{if .Greeting}}<p class="text" style="margin:0 0 12px;font-size:16px;line-height:1.6;color:#332A23">{{.Greeting}}</p>{{end}}
      {{range .Paragraphs}}<p class="text" style="margin:0 0 12px;font-size:16px;line-height:1.6;color:#332A23">{{.}}</p>{{end}}
      {{if .Code}}
      <p class="muted" style="margin:20px 0 8px;font-size:13px;font-weight:600;text-transform:uppercase;letter-spacing:1px;color:#6E645B;text-align:center">{{.CodeLabel}}</p>
      <div class="codebox code" dir="ltr" style="margin:0 auto 8px;background:#F6E3D9;color:#2B2118;border-radius:16px;padding:16px 8px;text-align:center;font-family:'SF Mono',Consolas,monospace;font-size:38px;font-weight:700;letter-spacing:10px">{{.Code}}</div>
      {{end}}
      {{if .ButtonURL}}
      <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="margin:24px 0 8px"><tr><td align="center">
        <a class="btn" href="{{.ButtonURL}}" style="display:inline-block;background:#B4583A;color:#FFFFFF;text-decoration:none;font-weight:700;font-size:17px;line-height:1;padding:16px 36px;border-radius:999px">{{.ButtonLabel}}</a>
      </td></tr></table>
      {{end}}
      {{if .Note}}<p class="muted" style="margin:16px 0 0;font-size:13px;line-height:1.5;color:#6E645B">{{.Note}}</p>{{end}}
    </td></tr>
    <tr><td align="center" class="muted" style="padding:20px 12px 0;font-family:Inter,'Segoe UI',Arial,sans-serif;font-size:12px;line-height:1.6;color:#6E645B">
      {{.Tagline}}<br>
      <a href="{{.SiteURL}}" style="color:#B4583A">public-speaking-lemon.vercel.app</a>
      {{if .UnsubURL}} &middot; <a href="{{.UnsubURL}}" style="color:#6E645B">{{.UnsubLabel}}</a>{{end}}
    </td></tr>
  </table>
</td></tr>
</table>
</body>
</html>`))

// render builds the HTML and a plain-text fallback from the same content.
func (s *Server) render(v emailView, subject string) mail.Message {
	v.SiteURL = s.cfg.SiteURL
	v.LogoURL = s.cfg.SiteURL + "/icons/Icon-192.png"
	v.Dir = "ltr"
	if v.Lang == "ar" {
		v.Dir = "rtl"
		v.Tagline = "مجتمع داعم يساعدك على التحدث بثقة."
	} else {
		v.Lang = "en"
		v.Tagline = "A supportive community for speaking with confidence."
	}
	var html bytes.Buffer
	_ = emailLayout.Execute(&html, v)

	var text strings.Builder
	text.WriteString(v.Title + "\n\n")
	if v.Greeting != "" {
		text.WriteString(v.Greeting + "\n\n")
	}
	for _, p := range v.Paragraphs {
		text.WriteString(p + "\n\n")
	}
	if v.Code != "" {
		text.WriteString(v.CodeLabel + ": " + v.Code + "\n\n")
	}
	if v.ButtonURL != "" {
		text.WriteString(v.ButtonLabel + ": " + v.ButtonURL + "\n\n")
	}
	if v.Note != "" {
		text.WriteString(v.Note + "\n\n")
	}
	text.WriteString("Speak Up Fearless\n" + v.SiteURL + "\n")
	if v.UnsubURL != "" {
		text.WriteString(v.UnsubLabel + ": " + v.UnsubURL + "\n")
	}

	m := mail.Message{Subject: subject, Text: text.String(), HTML: html.String()}
	if v.UnsubURL != "" {
		m.Headers = map[string]string{
			"List-Unsubscribe":      "<" + v.UnsubURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	}
	return m
}

func hello(name, lang string) string {
	name = strings.TrimSpace(strings.SplitN(strings.TrimSpace(name), " ", 2)[0])
	if lang == "ar" {
		if name == "" {
			return "مرحبًا،"
		}
		return "مرحبًا " + name + "،"
	}
	if name == "" {
		return "Hi there,"
	}
	return "Hi " + name + ","
}

func (s *Server) codeEmail(code, link, lang string) mail.Message {
	if lang == "ar" {
		return s.render(emailView{
			Lang:        "ar",
			Preheader:   "رمزك هو " + code,
			Title:       "أكّد بريدك الإلكتروني",
			Greeting:    "مرحبًا،",
			Paragraphs:  []string{"اضغط الزر لتأكيد بريدك الإلكتروني، أو أدخل هذا الرمز في الموقع."},
			Code:        code,
			CodeLabel:   "رمزك",
			ButtonLabel: "تأكيد بريدي الإلكتروني",
			ButtonURL:   link,
			Note:        "تنتهي صلاحية الرمز خلال 10 دقائق. إذا لم تقم بالتسجيل، يمكنك تجاهل هذه الرسالة.",
		}, "رمز Speak Up Fearless الخاص بك: "+code)
	}
	return s.render(emailView{
		Preheader:   "Your code is " + code,
		Title:       "Confirm your email",
		Greeting:    "Hi there,",
		Paragraphs:  []string{"Tap the button to verify your email, or enter this code on the site."},
		Code:        code,
		CodeLabel:   "Your code",
		ButtonLabel: "Verify my email",
		ButtonURL:   link,
		Note:        "It expires in 10 minutes. If you didn't sign up, you can ignore this email.",
	}, "Your Speak Up Fearless code: "+code)
}

func (s *Server) welcomeEmail(u userEmailInfo) mail.Message {
	if u.Lang == "ar" {
		return s.render(emailView{
			Lang:      "ar",
			Preheader: "شكرًا لانضمامك. هكذا تبدأ.",
			Title:     "أهلًا بك في Speak Up Fearless!",
			Greeting:  hello(u.Name, "ar"),
			Paragraphs: []string{
				"شكرًا لانضمامك إلينا. يسعدنا جدًا وجودك هنا.",
				"الخوف من التحدث أمام الناس شعور طبيعي تمامًا، وكل جولة تدريب صغيرة تجعله أخف. ابدأ بتحدي اليوم: دقيقة واحدة فقط.",
			},
			ButtonLabel: "ابدأ التدريب",
			ButtonURL:   s.cfg.SiteURL + "/games",
			Note:        "عندما تكون مستعدًا، يمكنك حجز مكالمة تعارف مجانية من الصفحة الرئيسية.",
			UnsubLabel:  "إيقاف رسائل البريد",
			UnsubURL:    u.Unsub,
		}, "أهلًا بك في Speak Up Fearless 🎉")
	}
	return s.render(emailView{
		Preheader: "Thanks for joining. Here's how to start.",
		Title:     "Welcome to Speak Up Fearless!",
		Greeting:  hello(u.Name, "en"),
		Paragraphs: []string{
			"Thank you for joining us. We're really glad you're here.",
			"Feeling nervous about speaking is completely normal, and every small practice round makes it lighter. Start with today's challenge: it only takes a minute.",
		},
		ButtonLabel: "Start practising",
		ButtonURL:   s.cfg.SiteURL + "/games",
		Note:        "Whenever you're ready, you can book a free intro call from the home page.",
		UnsubLabel:  "Stop these emails",
		UnsubURL:    u.Unsub,
	}, "Welcome to Speak Up Fearless 🎉")
}

func (s *Server) streakStartEmail(u userEmailInfo) mail.Message {
	if u.Lang == "ar" {
		return s.render(emailView{
			Lang:      "ar",
			Preheader: "عُد غدًا لتجعلها يومين.",
			Title:     "🔥 بدأت سلسلة جديدة!",
			Greeting:  hello(u.Name, "ar"),
			Paragraphs: []string{
				"أحسنت! أنهيت تحدي اليوم وبدأت سلسلة تدريب جديدة.",
				"عُد غدًا والعب جولة واحدة لتصبح سلسلتك يومين.",
			},
			ButtonLabel: "شاهد تقدّمي",
			ButtonURL:   s.cfg.SiteURL + "/account",
			UnsubLabel:  "إيقاف رسائل السلسلة",
			UnsubURL:    u.Unsub,
		}, "🔥 بدأت سلسلة جديدة!")
	}
	return s.render(emailView{
		Preheader: "Come back tomorrow to make it 2 days.",
		Title:     "🔥 You started a streak!",
		Greeting:  hello(u.Name, "en"),
		Paragraphs: []string{
			"Nice work! You finished today's challenge and started a new practice streak.",
			"Come back tomorrow and play one round to make it 2 days in a row.",
		},
		ButtonLabel: "See my progress",
		ButtonURL:   s.cfg.SiteURL + "/account",
		UnsubLabel:  "Stop streak emails",
		UnsubURL:    u.Unsub,
	}, "🔥 You started a streak!")
}

func (s *Server) streakReminderEmail(u userEmailInfo, days int) mail.Message {
	n := strconv.Itoa(days)
	if u.Lang == "ar" {
		return s.render(emailView{
			Lang:      "ar",
			Preheader: "جولة واحدة تحافظ على سلسلتك.",
			Title:     "سلسلتك (" + n + " أيام) تنتهي عند منتصف الليل",
			Greeting:  hello(u.Name, "ar"),
			Paragraphs: []string{
				"لم تلعب تحدي اليوم بعد.",
				"جولة واحدة سريعة تكفي للحفاظ على سلسلتك. أنت قادر على ذلك!",
			},
			ButtonLabel: "حافظ على سلسلتي",
			ButtonURL:   s.cfg.SiteURL + "/games",
			UnsubLabel:  "إيقاف رسائل السلسلة",
			UnsubURL:    u.Unsub,
		}, "⏰ سلسلتك ("+n+" أيام) تنتهي عند منتصف الليل")
	}
	unit := "days"
	if days == 1 {
		unit = "day"
	}
	return s.render(emailView{
		Preheader: "One quick round keeps it going.",
		Title:     "Your " + n + "-" + strings.TrimSuffix(unit, "s") + " streak ends at midnight",
		Greeting:  hello(u.Name, "en"),
		Paragraphs: []string{
			"You haven't played today's challenge yet.",
			"One quick round is all it takes to keep your " + n + " " + unit + " going. You've got this!",
		},
		ButtonLabel: "Keep my streak",
		ButtonURL:   s.cfg.SiteURL + "/games",
		UnsubLabel:  "Stop streak emails",
		UnsubURL:    u.Unsub,
	}, "⏰ Your "+n+"-"+strings.TrimSuffix(unit, "s")+" streak ends at midnight")
}

// userEmailInfo is what the personal emails need about the recipient.
type userEmailInfo struct {
	Name, Lang, Unsub string
}
