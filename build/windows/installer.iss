[Setup]
AppId={{D3C5C671-8409-4A44-B521-3F7499E11E10}
AppName=Discord Free Cloud
AppVersion=2.5.0
AppPublisher=zyrexdz
AppPublisherURL=https://github.com/zyrexdz/discord-free-cloud
AppSupportURL=https://github.com/zyrexdz/discord-free-cloud
AppUpdatesURL=https://github.com/zyrexdz/discord-free-cloud
DefaultDirName={autopf}\Discord Free Cloud
DisableProgramGroupPage=yes
OutputDir=..\..\dist
OutputBaseFilename=DiscordFreeCloudSetup
Compression=lzma2/ultra64
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=lowest
UninstallDisplayIcon={app}\discord-free-cloud.exe

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked
Name: "startup"; Description: "Start Discord Free Cloud when Windows starts"; GroupDescription: "Windows Integration:"
Name: "contextmenu"; Description: "Add Upload to Discord Free Cloud to right click menu"; GroupDescription: "Windows Integration:"

[Files]
Source: "..\..\discord-free-cloud.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\Discord Free Cloud"; Filename: "{app}\discord-free-cloud.exe"
Name: "{autodesktop}\Discord Free Cloud"; Filename: "{app}\discord-free-cloud.exe"; Tasks: desktopicon

[Registry]
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "DiscordFreeCloud"; ValueData: """{app}\discord-free-cloud.exe"""; Flags: uninsdeletevalue; Tasks: startup
Root: HKCU; Subkey: "Software\Classes\*\shell\DiscordFreeCloud"; ValueType: string; ValueName: ""; ValueData: "Upload to Discord Free Cloud"; Flags: uninsdeletekey; Tasks: contextmenu
Root: HKCU; Subkey: "Software\Classes\*\shell\DiscordFreeCloud\command"; ValueType: string; ValueName: ""; ValueData: """{app}\discord-free-cloud.exe"" ""%1"""; Flags: uninsdeletekey; Tasks: contextmenu
Root: HKCU; Subkey: "Software\Classes\Directory\shell\DiscordFreeCloud"; ValueType: string; ValueName: ""; ValueData: "Upload Folder to Discord Free Cloud"; Flags: uninsdeletekey; Tasks: contextmenu
Root: HKCU; Subkey: "Software\Classes\Directory\shell\DiscordFreeCloud\command"; ValueType: string; ValueName: ""; ValueData: """{app}\discord-free-cloud.exe"" ""%1"""; Flags: uninsdeletekey; Tasks: contextmenu

[Run]
Filename: "{app}\discord-free-cloud.exe"; Description: "{cm:LaunchProgram,Discord Free Cloud}"; Flags: nowait postinstall skipifsilent
