# speakUnshaken API

Go backend for the speakUnshaken site (Flutter web on Vercel at
https://public-speaking-lemon.vercel.app). It stores user profiles, practice
game history and booking requests.

- **Sign-in** stays with Firebase Auth (project `speakup-ca500`, Google and
  email/password). The Flutter app sends the user's Firebase ID token as
  `Authorization: Bearer <token>` and the API verifies it against Google's
  public keys. No service account is needed.
- **Database:** Postgres, hosted on [Neon](https://neon.tech) (free tier, no card).
  Tables are created automatically on startup (`internal/store/migrations`).
- **Hosting:** Google Cloud Run in the same Google Cloud project as Firebase.

## Why these choices

- **Cloud Run** lives in the Google account that already owns the Firebase
  project, scales to zero, and its free tier (2 million requests a month)
  covers a site this size. Render's free tier sleeps and takes ~50 s to wake;
  Fly.io now needs a card for anything.
- **Neon Postgres** is free without a card, doesn't pause data, and plain SQL
  fits users, history and bookings. Because it's ordinary Postgres, the API
  can move to any host or database provider later without code changes.

## API

All responses are JSON. Errors look like `{"error": "message"}`.

| Method | Path | Auth | What it does |
| --- | --- | --- | --- |
| GET | `/healthz` | none | `{"status":"ok"}`, or 503 if the database is down |
| GET | `/v1/me` | user | Current user's profile (created on first call), including `emailVerified` |
| PATCH | `/v1/me` | user | Update `timeZone` (IANA name, unknown ones ignored), `streakEmails` (bool), `displayName`, `language` (`"en"` or `"ar"`) and/or `avatar` (JPEG, PNG or WebP data URL up to 200 KB; `""` removes it). Profiles return `avatar` as the data URL or `null` |
| POST | `/v1/me/verify/send` | user | Email a 6-digit code to the user (English or Arabic by their language). Returns `{"sent":true}`, or `{"verified":true}` if the email is already verified (Google sign-ins). 1 per minute and 5 per hour per user, else 429 `{"retryAfter": seconds}`. 503 if email isn't configured |
| POST | `/v1/me/verify/check` | user | `{"code":"123456"}`. Returns `{"verified":true}`; a wrong code gives 400 `{"error":"wrong","attemptsLeft":n}`; an expired code or one with 5 misses gives 400 `{"error":"expired"}`. Codes last 10 minutes |
| GET | `/v1/verify/link?u=&c=` | none | The "Verify my email" button in the code email. Verifies with the code in the link (same expiry and attempt limits), then redirects to `SITE_URL/login?verify=verified|wrong|expired` |
| GET/POST | `/v1/cron/streak-reminders` | `Authorization: Bearer CRON_SECRET` | Run hourly (see `.github/workflows/streak-reminders.yml`). Emails people whose local time is 19:00 or later, who have a streak and haven't played the daily round today. At most once per person per day. Returns counts |
| GET/POST | `/v1/email/unsubscribe?u=&t=` | signed link | Turns off streak emails (link and List-Unsubscribe header in every streak and welcome email), then redirects to `SITE_URL/account?emails=off` |
| POST | `/v1/speech/analyze` | user | AI speaking coach. Multipart: `audio` (webm/opus, mp4/AAC, ogg, mp3 or wav, up to 4 MB), `topic`, `lang` (`en`/`ar`), `durationSeconds` (up to 125). ElevenLabs Scribe transcribes with word timings, the API measures pace, pauses and filler words, and Claude scores pace, pauses, fillers, clarity and flow 1-10 with feedback. The audio is never stored. Saves the report and a `speech-coach` game session. Errors `{"error","code"}`: 400 `no_speech`/`bad_audio`, 413 `too_large`, 429 `limit` (15 per user per 24 hours), 502 `upstream`, 503 `not_configured` |
| GET | `/v1/speech/reports?limit=` | user | The user's coach reports, newest first (summary: scores and overall) |
| GET | `/v1/speech/reports/{id}` | user | One full report: scores, metrics, moments, strengths, next step, timed transcript |
| POST | `/v1/games/sessions` | user | Record a finished round: `gameId`, `durationSeconds`, optional `score`, optional `details` object |
| GET | `/v1/games/sessions?gameId=&limit=` | user | Recent rounds, newest first (limit 1–200, default 50) |
| GET | `/v1/games/progress` | user | Per game: plays, total seconds, best score, last played |
| POST | `/v1/bookings` | optional | Booking request: `name`, `contact`, `preferredTime`, `reason` (why they want the call), optional `requestedCourse` (the session or package picked: `intro`, `single`, `four` or `eight`), optional `description`. Linked to the user when signed in. Limited to 5 per IP per 10 minutes |
| GET | `/v1/bookings` | user | The signed-in user's booking requests |
| GET | `/v1/admin/bookings?limit=` | admin | All booking requests |
| PATCH | `/v1/admin/bookings/{id}` | admin | Set `status` to `new`, `contacted`, `scheduled` or `closed` |

Game IDs match the Flutter catalog: `triple-step`, `conductor`, `podcast`,
`autocomplete`. Admins are the emails in `ADMIN_EMAILS`, and their Firebase
email must be verified (Google sign-in always is).

Calling it from Flutter:

```dart
final token = await FirebaseAuth.instance.currentUser!.getIdToken();
await http.post(
  Uri.parse('$apiBase/v1/games/sessions'),
  headers: {'Authorization': 'Bearer $token', 'Content-Type': 'application/json'},
  body: jsonEncode({'gameId': 'triple-step', 'durationSeconds': 90, 'score': 4}),
);
```

## Configuration

| Variable | Default | Notes |
| --- | --- | --- |
| `PORT` | `8080` | Cloud Run sets this |
| `FIREBASE_PROJECT_ID` | `speakup-ca500` | Tokens from other projects are rejected |
| `DATABASE_URL` | empty | Neon connection string. Empty means an in-memory store (data lost on restart) |
| `ALLOWED_ORIGINS` | `https://public-speaking-lemon.vercel.app` | Comma-separated exact origins for CORS |
| `ALLOW_LOCALHOST` | `true` | Also allow `http://localhost:<port>` for local Flutter runs |
| `ADMIN_EMAILS` | empty | Comma-separated |
| `STREAK_EMAILS` | `off` | Welcome and streak emails: `off`, `test` (only the addresses in `STREAK_EMAILS_TEST`) or `on` (everyone who hasn't opted out). Verification codes are not affected |
| `STREAK_EMAILS_TEST` | empty | Comma-separated addresses that get emails in `test` mode |
| `CRON_SECRET` | empty | Long random string. Protects the reminder endpoint and signs unsubscribe links; welcome and streak emails stay off without it. Also add it as a GitHub Actions repository secret with the same name |
| `DEFAULT_TIME_ZONE` | `America/Toronto` | Used for people whose browser hasn't sent a time zone yet |
| `DEEPGRAM_API_KEY` | empty | Backup speech-to-text (Deepgram nova-3), or the only one if ElevenLabs isn't set. The coach is off (503) until a speech-to-text key and `ANTHROPIC_API_KEY` are set |
| `DEEPGRAM_MODEL` | `nova-3` | Deepgram speech-to-text model |
| `ELEVENLABS_API_KEY` | empty | Speech-to-text (ElevenLabs Scribe, keeps every filler word). Used first when set; Deepgram is the backup |
| `ANTHROPIC_API_KEY` | empty | Claude, which writes the coach's scores and feedback |
| `CLAUDE_COACH_MODEL` | `claude-sonnet-5-5` | Claude model for the coach (e.g. `claude-opus-5-5`) |
| `ELEVENLABS_STT_MODEL` | `scribe_v2` | ElevenLabs speech-to-text model |
| `BREVO_API_KEY` | empty | Send verification emails through Brevo (free, 300/day) instead of SMTP. Needs `MAIL_FROM` set to a sender confirmed in Brevo |
| `SITE_URL` | `https://speakup-ca500.web.app` | Where the email button sends people after verifying |
| `PUBLIC_URL` | `https://public-speaking-backend.vercel.app` | This API's address, used in the email button link |
| `SMTP_HOST` | empty | e.g. `smtp.gmail.com`. Verification emails are off until host, user, password and sender are all set |
| `SMTP_PORT` | `587` | 587 uses STARTTLS, 465 uses TLS |
| `SMTP_USER` | empty | e.g. your Gmail address |
| `SMTP_PASS` | empty | For Gmail, an App Password (Google Account > Security > 2-Step Verification > App passwords), not your normal password |
| `MAIL_FROM` | empty | e.g. `speakUnshaken <you@gmail.com>`; with Gmail it must be the same address as `SMTP_USER` |

See `.env.example`.

## Run locally

Needs Go 1.25+. Without `DATABASE_URL` it runs on an in-memory store, which is
enough to try the API.

```powershell
go run ./cmd/api
```

```bash
curl http://localhost:8080/healthz
```

With a real database (PowerShell):

```powershell
$env:DATABASE_URL = "postgresql://user:pass@ep-xxx.neon.tech/neondb?sslmode=require"
go run ./cmd/api
```

Tests:

```bash
go test ./...
```

The store tests also run against Postgres when `TEST_DATABASE_URL` points at a
throwaway database (a Neon branch works well).

## Deploy to Vercel

Vercel runs this repo as a Go server with no extra config. In the Vercel
project, open Settings > Environment Variables and set:

- `DATABASE_URL`: the Neon connection string (required; without it every
  serverless instance keeps its own throwaway in-memory data)
- `ADMIN_EMAILS`: emails that can manage bookings
- `ALLOW_LOCALHOST`: `false` for Production

Redeploy after changing variables, then open `/healthz`. It should say
`"database": "postgres"`. A request log line for `/favicon.ico` is just the
browser asking for an icon and can be ignored.

## Deploy to Cloud Run

One-time setup, done by the project owner:

1. **Turn on billing for `speakup-ca500`.** In the Firebase console, upgrade the
   project to the Blaze (pay-as-you-go) plan, or link a billing account in the
   Google Cloud console. Cloud Run requires it even when usage stays inside the
   free tier. Add a budget alert (for example $5) under Billing > Budgets.
2. **Create the database.** Sign up at neon.tech, create a project (pick a
   region near `us-central1`, such as AWS us-east-1), and copy the connection
   string.
3. **Install the gcloud CLI** and sign in:

   ```bash
   gcloud auth login
   gcloud config set project speakup-ca500
   gcloud services enable run.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com secretmanager.googleapis.com
   ```

4. **Store the database URL as a secret** and let Cloud Run read it:

   ```bash
   printf '%s' 'postgresql://...neon.tech/neondb?sslmode=require' | gcloud secrets create database-url --data-file=-
   ```

   ```bash
   gcloud secrets add-iam-policy-binding database-url --member="serviceAccount:$(gcloud projects describe speakup-ca500 --format='value(projectNumber)')-compute@developer.gserviceaccount.com" --role=roles/secretmanager.secretAccessor
   ```

Deploy (and redeploy after changes) from this folder:

```bash
gcloud run deploy speakup-api --source . --region us-central1 --allow-unauthenticated --set-secrets DATABASE_URL=database-url:latest --set-env-vars "FIREBASE_PROJECT_ID=speakup-ca500,ALLOWED_ORIGINS=https://public-speaking-lemon.vercel.app,ALLOW_LOCALHOST=false,ADMIN_EMAILS=you@example.com" --min-instances 0 --max-instances 3
```

`--allow-unauthenticated` lets browsers reach the service; the API does its own
Firebase sign-in checks per route. The command prints the service URL
(`https://speakup-api-....run.app`); check it with `/healthz`, then give that
URL to the Flutter app as its API base.

If you add a custom domain or Vercel preview URLs, add them to
`ALLOWED_ORIGINS`.

## Layout

```
cmd/api/              entry point, config from env
internal/auth/        Firebase ID token verification
internal/api/         routes, CORS, auth middleware, handlers
internal/store/       Store interface, Postgres and in-memory implementations, SQL migrations
```
