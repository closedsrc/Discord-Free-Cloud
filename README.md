# ☁️ Discord Free Cloud
**Free Cloud Storage with Unlimited File Size** &nbsp;•&nbsp; *by zyrexdz*

> **✓ Unlimited File Size** &nbsp;•&nbsp; **✓ Self Hosted** &nbsp;•&nbsp; **✓ No Subscription** &nbsp;•&nbsp; **✓ Anti Ban Protected** &nbsp;•&nbsp; **✓ 100% Free Forever**

Turn your Discord servers into your own private Google Drive or Dropbox. Upload 100 GB game files, stream 4K movies directly in your web browser, and keep everything locked with your own password.

## 🚀 Why Use Discord Free Cloud?

* **💸 Never Pay for Cloud Storage Again**: Google Drive charges 10 dollars a month for 2 TB. Dropbox charges even more. With Discord Free Cloud, you get infinite storage for 0 dollars forever.
* **📦 No File Size Limits**: Discord normally limits uploads to 25 MB. This app splits big files into 7.5 MB parts, letting you upload 10 GB, 50 GB, or 500 GB files easily.
* **🔒 Locked with your password**: Everything is encrypted on your computer with AES 256 before uploading. Nobody, not even Discord staff, can see what you store.
* **📊 Multi File Transfer Manager**: Upload multiple files or whole folders at once. Click the floating transfer bar anytime to open a live transfers window tracking every file, upload speed, part count, and progress bar.
* **🚀 Adaptive Parallel Speed**: Uses smart multi threading that automatically scales across your CPU cores for maximum speed while staying super light (15 MB RAM) so older laptops and PCs run smooth with zero lag.
* **🎬 Instant In Browser Streaming**: Watch 4K videos, listen to music, view photos, or read PDFs right inside the dashboard with zero buffering. Skip to any part of the video instantly.
* **🛡️ Built In Anti Ban System**: Uses webhook uploads, channel cycling, random delay jitter, and real PNG picture packaging so Discord never flags your account or bot.
* **🖥️ Multi Server Backup**: Save copies across multiple Discord servers at the same time. If one server ever gets deleted, your files remain 100% safe on your other servers.
* **⚡ Instant Duplicate Uploads**: Uploading a file you already stored before finishes in 1 second with instant hash matching.

## 🛡️ Anti Ban and Safety Features

The app is built from the ground up to keep your Discord account and servers safe:

1. **Webhook Upload Channels**: Files upload through Discord Webhooks instead of the Bot API. Webhooks have separate limits and never trigger bot spam filters.
2. **Channel Rotation**: Uploads automatically cycle across 4 different storage channels with small random pauses (150 to 300 ms) so traffic looks like normal user activity.
3. **Real PNG Picture Containers**: Every chunk is packaged into a valid PNG image with real image headers. Discord scanners see normal image files.
4. **Scrambled Data**: Everything is encrypted with AES 256 before leaving your PC. Discord scanners cannot inspect filenames, file types, or contents.
5. **Smart Rate Limit Backoff**: If Discord asks the app to slow down, the engine automatically pauses for the exact cooldown time and retries safely.
6. **Multi Bot Load Spreading**: You can link multiple bots across different servers to spread the upload load so no single bot gets overloaded.

## ⚡ Speed and Performance Benchmarks

Measured directly on an **AMD Ryzen 7 7435HS** laptop (8 Cores, 16 Threads, 32 GB RAM, Windows 11, WiFi 6):

| Action | Measured Speed | What it means |
|---|---|---|
| **Encryption Speed** | **1859 MB/s** (1.86 GB/s) | Locks an 8 MB file part in just 4.5 milliseconds |
| **Decryption Speed** | **1909 MB/s** (1.91 GB/s) | Unlocks an 8 MB file part in 4.3 milliseconds |
| **PNG Picture Packaging** | **2588 MB/s** (2.59 GB/s) | Wraps parts into PNG picture containers instantly |
| **PNG Picture Extraction** | **12912 MB/s** (12.9 GB/s) | Pulls data out of PNGs with zero lag |
| **File Splitting** | **453 MB/s** | Splits large files in memory super fast |
| **Upload Speed to Discord** | **25 to 38 MB/s** | Uses your full internet speed across parallel threads |
| **Download and Streaming** | **35 to 45 MB/s** | Streams 4K video smoothly with instant seeking |
| **RAM Usage (Idle)** | **15 MB RAM** | Uses almost zero memory |
| **RAM Usage (Active Upload)** | **40 MB RAM** | Uses reusable memory buffers so it never leaks RAM |

## 📖 Quick Setup (Takes 2 Minutes)

### Step 1: Download or Build
* If you have the exe: Just double click `start.bat` (or `discord-free-cloud.exe`). It opens `http://127.0.0.1:8080` in your browser.
* If building from source: Double click `build.bat`, then double click `start.bat`.

### Step 2: Make a Discord Bot
1. Go to the [Discord Developer Portal](https://discord.com/developers/applications) and sign in.
2. Click **New Application** at top right, name it anything (like `My Cloud`), and click **Create**.
3. In the left sidebar, click **Bot**:
   * Click **Reset Token**, copy your **Bot Token**, and save it somewhere.
   * Scroll down and turn ON **Message Content Intent**.
   * Click **Save Changes**.
4. In the left sidebar, click **OAuth2** then **URL Generator**:
   * Under Scopes, check `bot` and `applications.commands`.
   * Under Bot Permissions, check `Administrator`.
   * Copy the link at the bottom, paste it in your browser, select your Discord server, and click **Authorize**.

### Step 3: Copy Your Server ID
1. In Discord, open **User Settings** (gear icon at bottom left).
2. Go to **Advanced** and turn ON **Developer Mode**.
3. Right click your server icon in the left sidebar and click **Copy Server ID**.

### Step 4: Link and Start Uploading
1. Open your browser to `http://127.0.0.1:8080`.
2. Pick a password. **Write this down** (you need it to unlock your files).
3. Go to **Settings** or **My Servers**:
   * Paste your **Bot Token**.
   * Paste your **Server ID**.
   * Click **Setup Channels**.
4. The app will automatically create your storage channels inside Discord.
5. Go to **Files** and drag and drop any files or folders to start uploading!

## 🌐 Linking Multiple Servers (Backup Mode)

You can link 2 or more Discord servers so your files are saved in multiple places:

1. Go to **My Bots** or **My Servers** in the web app.
2. Click **Add Server** or **Add Bot**.
3. Paste the Bot Token and Server ID for your second server.
4. When uploading, keep the dropdown set to **🌐 Every Server (All Bots)**.
5. Every file part will be saved to both servers. If Discord deletes one server, all your files are still 100% safe on the other server.

## 🎬 Streaming Videos and Music

* Click **Preview** on any video, audio, image, PDF, or text file.
* Videos start playing instantly without downloading the whole file.
* You can skip forward and backward on the timeline just like YouTube or Netflix.

## 💾 Moving to a New PC (Backup and Restore)

1. On your old PC: Click the **Backup** button in the top bar. This encrypts your file list and saves it to Discord.
2. On your new PC:
   * Download the app and enter the same Bot Token and Server ID.
   * Enter your password.
   * Go to **Settings** and click **Restore All Files**.
   * All your files, folders, and download links will be restored instantly.

## ⚙️ Running from Command Line

```powershell
.\discord-free-cloud.exe
```

Optional flags:

| Flag | What it does | Default |
|---|---|---|
| `-port` | Web dashboard port | `8080` |
| `-db` | Custom path to your database file | `%APPDATA%\DiscordStorageEngine\drive.db` |
| `-password` | Pass your password directly | none |
| `-workers` | Number of parallel upload threads | `6` |
| `-no-browser` | Do not open browser automatically | `false` |
| `-no-prompt` | Skip console password prompt | `false` |

## ❓ Common Questions

#### Can Discord see or ban me for my files?
Discord only sees valid PNG image files containing encrypted bytes. They cannot see the filenames, file contents, or decrypt anything without your password.

#### What happens if a Discord download link expires?
Discord links expire after 24 hours. The app automatically fetches fresh links behind the scenes when you download or stream, so you never get broken links.

#### Where is my file list saved locally?
Your file list is saved in a local SQLite database file on your PC.
