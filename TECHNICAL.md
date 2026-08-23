# Discord Free Cloud
*by zyrexdz*

Free, private cloud storage using Discord. Files are split into parts, locked with your password, disguised as PNG pictures, and uploaded across Discord channels. Nobody can see or open your files without your password. You can link multiple Discord servers so your files stay safe even if one server goes down.

## How it works

When you upload a file:
1. The app reads your file from your PC
2. Splits it into 7.5 MB parts
3. Locks each part with your password using AES 256
4. Wraps each locked part inside a real PNG picture format so Discord does not mess with it
5. Uploads the parts to your Discord channels using webhooks
6. Saves what parts belong to what file in a local SQLite database file

When you download:
1. Grabs the PNG pictures back from Discord
2. Pulls the file data out of the PNG
3. Unlocks the parts with your password
4. Puts the parts back together in order and streams the file directly to your browser

## Project Structure

```
discord-free-cloud/
├── cmd/app/main.go              Main entry point, flags, console password, web server boot
├── frontend/                    Embedded web UI (HTML, CSS, JS)
│   ├── index.html               Main dashboard
│   ├── style.css                Clean dark mode styling
│   ├── app.js                   Frontend logic, WebSockets, upload and download UI
│   └── frontend.go              Go embed directive
├── internal/
│   ├── crypto/                  Password locking and PNG wrapping
│   ├── db/                      Local SQLite database queries
│   ├── discord/                 Discord API calls (webhooks, channels, auto setup)
│   ├── uploader/                Multi thread file uploader
│   ├── downloader/              Multi thread file downloader and streaming
│   ├── catalog/                 Encrypted cloud backup snapshots
│   ├── server/                  HTTP routes and WebSocket
│   ├── storage/                 Server health and latency checks
│   ├── chunker/                 File splitting logic
│   └── syswin/                  Windows utilities (disk space, browser launcher)
├── build.bat                    One click build script
├── start.bat                    One click start script
└── data/                        Default database folder
```

## Upload and Download Pipeline

### Uploading
* 6 parallel worker threads grab file parts, lock them, wrap them in PNGs, and send them to Discord webhooks
* Uses a reusable memory pool so your computer RAM stays low (about 40 MB)
* Broadcasts live progress, speed in MB per second, and ETA to the browser over WebSockets
* If an exact duplicate part was already uploaded previously, it finishes instantly without re-uploading

### Downloading and Streaming
* Downloads up to 8 parts at the same time
* Puts parts in correct order and streams them straight to your browser
* Supports video seeking so you can skip around in 4K movies without downloading the whole file first

## Password Security

* **Password Key**: Your password is mixed with a random salt to create a 32 byte key
* **Part Locking**: AES 256 locks each part with a fresh random key nonce
* **PNG Packaging**: Real PNG headers make Discord treat each part like a normal picture
* **Safe Logins**: Only the hash of your key is saved locally to check your password on startup

## Database Tables

All information is kept inside `drive.db`:

| Table | What it stores |
|---|---|
| `settings` | Your preferences, saved password hash, worker count |
| `files` | Your files and folders (name, size, type, modified date) |
| `chunks` | File parts (Discord message ID, channel ID, attachment URL, hash) |
| `jobs` | Active uploads and downloads |
| `channels` | Storage channels and webhooks on your Discord servers |
| `bot_nodes` | Connected Discord bots and servers |

## Multi Server Backup

* **Save to All Servers**: Uploads parts to all linked servers so you have backup copies
* **Specific Server**: Uploads only to one selected server
* **Automatic Fallback**: If a Discord download link is dead or a server is unreachable, the app automatically pulls the part from your other servers

## Anti Ban Protections

* **Webhook Transport**: Uploads through channel webhooks to avoid bot rate limits
* **Round Robin Channels**: Cycles through 4 channels with 150 to 300 ms random delay jitter
* **PNG Pictures**: Disguises encrypted bytes inside valid PNG image headers so Discord scanners see real pictures
* **Client Side AES**: Discord cannot read file names or keywords because everything is encrypted before upload
* **Smart Backoff**: Automatically respects Discord rate limits with cooldown timers
* **Multi Bot Support**: Distribute uploads across different bots and servers

## HTTP API

| Method | Endpoint | What it does |
|---|---|---|
| GET | `/api/status` | Current file count, storage used, disk space, and memory |
| GET/POST | `/api/settings` | Read or update settings |
| POST | `/api/auto-setup` | Automatically create Discord channels and webhooks |
| GET | `/api/files` | Get list of files in a folder |
| POST | `/api/files/create_text` | Create a new text file directly |
| POST | `/api/folders/create` | Create a virtual folder |
| POST | `/api/upload/file` | Upload a file |
| POST | `/api/upload/dir` | Upload an entire folder |
| GET | `/api/download?file_id=` | Stream download or video preview |
| POST | `/api/delete` | Delete a file or folder |
| POST | `/api/catalog/sync` | Save an encrypted backup to Discord |
| POST | `/api/catalog/restore` | Restore files from Discord backup |
| GET | `/api/servers` | List all linked Discord servers and status |
| GET | `/api/servers/health` | Ping test all servers |
| GET | `/api/auth/status` | Check if drive is unlocked |
| POST | `/api/auth/unlock` | Unlock with password |
| WS | `/ws` | Live WebSocket updates |

## Command Line Options

```powershell
.\discord-free-cloud.exe
```

* `/port`: Web dashboard port (default: 8080)
* `/db`: Custom path to database file
* `/password`: Master password flag
* `/workers`: Number of parallel upload threads (default: 6)
* `/no-browser`: Do not open browser automatically
