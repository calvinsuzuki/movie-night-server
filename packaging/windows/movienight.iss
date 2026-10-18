; Inno Setup script for the Movie night Windows installer.
; Built by build.ps1, which passes /DAppVersion=... (and /DOutputName=nightly for
; nightly builds) and expects dist\windows\ to be filled.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef OutputName
  #define OutputName AppVersion
#endif
#define AppName "Movie night"
#define AppExe "movienight.exe"

[Setup]
AppId={{6F1C7E2A-3D5B-4E8F-9A61-2B7C4D9E0F13}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=calvinsuzuki
DefaultDirName={autopf}\Movie night
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
; Admin is needed to add the firewall rule that lets video reach friends directly.
PrivilegesRequired=admin
UsedUserAreasWarning=no
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0.19041
SetupIconFile=..\..\assets\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
WizardStyle=modern
Compression=lzma2/ultra64
SolidCompression=yes
OutputDir=..\..\dist
OutputBaseFilename=MovieNight-Setup-{#OutputName}
CloseApplications=force

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Shortcuts:"
Name: "autostart"; Description: "Start Movie night when I sign in to Windows"; GroupDescription: "Startup:"; Flags: unchecked

[Files]
Source: "..\..\dist\windows\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\dist\windows\ffmpeg.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\dist\windows\cloudflared.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Registry]
; Same value the tray's "Start with Windows" toggle writes; always removed on uninstall.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "MovieNight"; ValueData: """{app}\{#AppExe}"""; Tasks: autostart
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: none; ValueName: "MovieNight"; Flags: uninsdeletevalue dontcreatekey

[Run]
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""Movie night"""; Flags: runhidden; StatusMsg: "Configuring firewall..."
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""Movie night"" dir=in action=allow program=""{app}\{#AppExe}"" enable=yes profile=any"; Flags: runhidden; StatusMsg: "Configuring firewall..."
Filename: "{app}\{#AppExe}"; Parameters: "-guide"; Description: "Show me how to invite friends"; Flags: nowait postinstall skipifsilent runasoriginaluser
Filename: "{app}\{#AppExe}"; Description: "Launch {#AppName} now"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/f /im {#AppExe}"; Flags: runhidden; RunOnceId: "KillApp"
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""Movie night"""; Flags: runhidden; RunOnceId: "DelFirewall"
