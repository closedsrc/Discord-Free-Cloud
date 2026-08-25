# Discord Free Cloud

Turn your Discord servers into private, unlimited cloud storage with end-to-end encryption and instant in-browser 4K media streaming.

[![Go Version](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](https://golang.org)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

![Dashboard Demo](demo.gif)

---

## Quickstart

```powershell
git clone https://github.com/zyrexdz/Discord-Free-Cloud.git
cd Discord-Free-Cloud
.\start.bat
```

Double click `start.bat` anytime to launch the dashboard at `http://127.0.0.1:8080`.

---

## Cloud Storage Comparison

| Feature | Discord Free Cloud | Google Drive | Dropbox | Mega |
|---|---|---|---|---|
| **Monthly Price** | **$0 (Free Forever)** | $9.99 / mo (2 TB) | $11.99 / mo (2 TB) | $10.99 / mo (2 TB) |
| **Total Storage** | **Unlimited ∞** | 15 GB cap | 2 GB cap | 20 GB cap |
| **Max File Size** | **Unlimited (500 GB+)** | 15 GB cap | 2 GB cap | 20 GB cap |
| **In-Browser 4K Video** | **Instant (Zero wait)** | Re-encodes video | Slow buffering | Limited |
| **Encryption Keys** | **Only You (Local PC)** | Google Staff | Dropbox Staff | Mega |
| **Multi-Server Redundancy** | **Yes** | No | No | No |
| **Telemetry & Tracking** | **Zero (100% Private)** | Ad tracking | Tracked | Tracked |

---

## Features

- **No File Size Caps**: Splits large files into 7.5 MB parts automatically, letting you store 10 GB, 50 GB, or 500 GB+ files without hitting Discord's 25 MB limit.
- **Client-Side AES-256-GCM**: Everything is encrypted with AES-256-GCM on your computer before uploading. Discord never sees raw filenames, file types, or contents.
- **Instant In-Browser Streaming**: Watch 4K video, listen to music, view images, or read PDFs right inside the browser with fast timeline seeking.
- **Anti-Ban Protection**: Uses webhooks instead of the Bot API, cycles across storage channels with random jitter, and wraps chunks into valid PNG image containers.
- **Multi-Server Backup**: Replicate file chunks across multiple Discord servers. If one server is lost, your files remain safe on the others.
- **Instant Deduplication**: Re-uploading an existing file finishes in 1 second via SHA-256 hash matching.
- **Lightweight**: Uses ~15 MB RAM idle and ~40 MB during parallel transfers with reusable buffer pools.

---

## Performance Benchmarks

Measured on an AMD Ryzen 7 7435HS (8 Cores, 16 Threads, Windows 11, WiFi 6):

| Operation | Throughput | Notes |
|---|---|---|
| **AES-256 Encryption** | **1.86 GB/s** | Locks an 8 MB chunk in ~4.5 ms |
| **AES-256 Decryption** | **1.91 GB/s** | Unlocks an 8 MB chunk in ~4.3 ms |
| **PNG Container Wrapping** | **2.59 GB/s** | Formats encrypted bytes into valid PNG structure |
| **PNG Container Extraction** | **12.9 GB/s** | Pulls raw payload with zero overhead |
| **Upload Speed** | **25 – 38 MB/s** | Parallel worker pipeline across webhooks |
| **Download & Stream Speed** | **35 – 45 MB/s** | Fast chunk prefetching and range requests |
| **Idle Memory** | **~15 MB RAM** | Zero background memory bloat |

---

## Setup Guide

### 1. Create a Discord Bot
1. Go to the [Discord Developer Portal](https://discord.com/developers/applications) and create a **New Application**.
2. Under **Bot**, click **Reset Token**, copy your **Bot Token**, and enable **Message Content Intent**.
3. Under **OAuth2 > URL Generator**, select `bot` + `applications.commands` with `Administrator` permissions. Open the generated link to invite the bot to your server.

### 2. Get Your Server ID
1. In Discord settings, go to **Advanced** and enable **Developer Mode**.
2. Right-click your server icon and select **Copy Server ID**.

### 3. Connect & Upload
1. Open `http://127.0.0.1:8080`.
2. Set your encryption password (used locally to derive your AES-256 key).
3. Paste your **Bot Token** and click **Verify Bot**, then click **Invite Bot** to link it to your server.
4. Drag and drop any files or folders to start uploading.

---

## Command Line Flags

```powershell
.\discord-free-cloud.exe [flags]
```

| Flag | Description | Default |
|---|---|---|
| `-port` | Web dashboard port | `8080` |
| `-db` | Database file location | `%APPDATA%\DiscordFreeCloud\drive.db` |
| `-password` | Encryption password (bypass prompt) | none |
| `-workers` | Number of parallel worker threads | `6` |
| `-no-browser` | Disable automatic browser launch | `false` |
| `-no-prompt` | Skip interactive console password prompt | `false` |

---

## FAQ

#### Can Discord see what I upload?
No. Every file chunk is encrypted with AES-256-GCM and polyglot-wrapped as a valid PNG image before transmission. Discord scanners only see standard image files with encrypted binary payloads.

#### What happens when Discord attachment links expire?
Discord attachment URLs expire after 24 hours. The engine automatically requests fresh authenticated attachment URLs on demand during downloads or streaming.

---

If this saved you time, leave a ⭐ to help others find it.
