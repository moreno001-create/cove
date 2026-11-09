# Cove

The household's own cloud: files and photos, office documents, calendars, and the everyday things of a home, in one light, modern web app. People sign in with their
Homeport account; phones and computers sync with app passwords.

- **Files**, copyparty-style: drag in files or whole folders; big files go up in 8 MB pieces, three at a time, and carry
  on after a dropped connection or a reload. A list or a thumbnail grid, a photo viewer (arrow keys, swipe), a music
  player that plays the folder, PDFs, and text and Markdown you can edit in the browser. Trash kept 30 days, zip
  downloads, search by name.
- **Share links** to a file or folder, with an optional password and an end date. A folder link can let people add
  files, or be a **drop box** where they can upload but can't see what's inside.
- **Calendar**: month and list views, several calendars with colors, repeating events. Share a calendar with the
  household (to view or to change), like a family calendar. Everything syncs to iPhone, Mac, Android (DAVx⁵) and
  Thunderbird over **CalDAV** (`/dav/cal/`).
- **Tasks**: to-do lists, shareable like calendars (groceries, chores), with due dates and notes. They sync to iPhone
  Reminders and other CalDAV task apps.
- **Contacts**: a searchable address book that syncs to phones and Macs over **CardDAV** (`/dav/card/`).
- **On phones**: installs as an app (Add to Home Screen). On Android, Cove is then in the share sheet: shared photos,
  files and links land in "Shared to Cove".
- **Office documents** (Word, Excel, PowerPoint, OpenDocument) open and save in **Collabora Online**, through WOPI.
- **Home page**: today and this week, tasks due, recent documents, what was shared with you, starred files and folders.
- **Search everything** from one box: names, the words inside documents (text, Markdown, Word, Excel, PowerPoint,
  OpenDocument, PDFs with readable text), events, tasks and contacts.
- **Calendar extras**: week and day views (drag to move, drag the bottom edge to resize), reminders, inviting people
  in the household (they answer going / maybe / can't go), a birthdays calendar from contacts, .ics import and export
  (importing again doesn't make copies).
- **Contacts** import and export as .vcf.
- **Notifications** under the bell (and in the browser, and on phones through ntfy): shares, changes to shared
  calendars and lists, edits to your documents, uploads to your links, invitations and reminders.
- **Notes**: Markdown notes with checklists you tick and pins, kept as plain files in "Notes".
- **Trash for everything**: deleted events, tasks and contacts (also when deleted on a phone) stay 30 days, like files.
- **Links**: QR codes and download counts.
- **Admin** (Homeport's admins): space per person, storage limits, and every link and share in one place.
- **WebDAV** (`/dav/files/`) for Finder, Windows, rclone and phone file apps. Deleting there goes to the trash too.
- **Fridge**: the household's notice board: notes, lists anyone ticks, photos, polls and countdowns.
- **Chat**: a Family room with everyone and rooms for some of the household, with photos.
- **Photos**: every picture in your files on one timeline (by when it was taken), albums (shared albums take
  everyone's photos), a map of where they were taken, and backing up a phone's camera roll (skipping what's there).
- **Scanning**: photograph pages with a phone, drag the corners to straighten them, save a PDF. The words on scanned
  pages and on photos of papers are read (Tesseract), so search finds receipts and letters.
- **Meals**: a week's plan and a recipe box; the week's ingredients go onto the fridge's shopping list in one tap.
- **Chores**: who does what (taking turns if you like), ticking them off, points, streaks and allowance.
- **Trips**: the plan by day with bookings, packing lists per person, tickets and papers.
- **Home records**: appliances, warranties, serial numbers, passports, with receipts and manuals attached, and
  reminders before warranties and documents run out.
- **Budget**: bills with reminders, shared spending split between people, and who owes whom.
- **Ask Cove**: questions answered from the household's calendar, tasks, fridge, chores, meals, trips, records and
  documents by an AI server on the home network (LM Studio, Ollama); "Ask Cove" on a document summarizes it.
  **Smart search** finds documents by meaning, with that server's embedding model.
- **Kids' accounts** (set by an admin): a simpler Cove without money, home records or public links.
- **Ask Cove does things**: "add milk to the shopping list", "put the dentist on Thursday at 3", "give Sam the dishes",
  "turn off the porch light": it carries them out as you (with your rights) and says what it did. A microphone button
  asks by voice (the browser's speech recognition).
- **Morning briefing**: one notification at the time you choose: weather, events, dinner, your chores, medicines, bills
  due, countdowns.
- **Home Assistant**: lights, locks, the garage door and sensors on the home page and the kitchen display (kids can't
  open locks or doors), solar and battery numbers (EG4 or any inverter Home Assistant knows), and the weather.
- **Jellyfin**: "Continue watching" and what's new, on the home page.
- **Kitchen display**: a page for a tablet on the wall: clock and weather, the week with dinners, today's chores to
  tick, devices, the fridge, and a photo slideshow from an album.
- **Wish lists**: everyone's list; "I'll get it" so nobody buys twice, and gift ideas the person never sees.
- **Health**: medicines with dose reminders (for people and pets) and doses anyone can tick; checkups, vaccines and
  vet visits with reminders when they're due again.
- **Pantry**: scan barcodes with the phone (names from Open Food Facts), plus and minus; running low puts it on the
  shopping list; "use it soon" reminders.
- **Journal and memories**: a family journal with photos, and "on this day" photos and entries from past years.
- **Off-site backup**: each night, files and database snapshots go to B2, S3 or another computer, encrypted with restic.

## Running it

It's meant to be installed from Homeport's catalog, which fills in everything. By hand:

| Variable | |
|---|---|
| `COVE_URL` | the address people open, `https://cove.example.com` (required) |
| `COVE_OIDC_ISSUER`, `COVE_OIDC_CLIENT_ID`, `COVE_OIDC_CLIENT_SECRET` | Homeport's sign-in (OpenID Connect) |
| `COVE_COLLABORA` | Collabora Online's address, for documents (optional) |
| `COVE_DATA` | where files and the database go (default `/data`) |
| `COVE_ADDR` | where it listens (default `:8080`) |
| `COVE_AI_URL`, `COVE_AI_MODEL`, `COVE_AI_EMBED_MODEL` | the home AI server (`http://host:1234/v1`) and its models; also set in Admin → AI |
| `COVE_RESTIC` | restic's program, for off-site backups (default `restic`, as in the image) |
| `COVE_TESSERACT`, `COVE_OCR_LANG` | Tesseract's program (default `tesseract`, as in the image) and languages (default `eng`) |
| `COVE_DEV_USER` | try it out without sign-in, as this user (never on a server others can reach) |

```sh
COVE_URL=http://localhost:8080 COVE_DATA=./data COVE_DEV_USER=me go run ./cmd/cove
```

Files are plain files in `data/files/<user>/`, so backups and other tools see them as they are. Calendars and
everything else live in `data/cove.db` (SQLite).

## Developing

```sh
go test ./...   # files, sharing, versions, WebDAV, CalDAV, CardDAV, events, tasks, contacts, WOPI and sign-in
```

The server is Go (`internal/app`); the web app is plain JavaScript with no build step (`internal/app/web`).
