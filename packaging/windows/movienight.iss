; Inno Setup script for the Movie night Windows installer.
; Built by build.ps1, which passes /DAppVersion=... and expects dist\windows\ to be filled.

#ifndef AppVersion
  #define AppVersion "1.0.0"
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
; Admin is needed to add the firewall rule friends' connections come through.
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
OutputBaseFilename=MovieNight-Setup-{#AppVersion}
CloseApplications=force

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Shortcuts:"
Name: "autostart"; Description: "Start Movie night when I sign in to Windows"; GroupDescription: "Startup:"; Flags: unchecked

[Files]
Source: "..\..\dist\windows\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\dist\windows\ffmpeg.exe"; DestDir: "{app}"; Flags: ignoreversion

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
Filename: "{app}\{#AppExe}"; Parameters: "-guide"; Description: "Show me how to set up Tailscale and invite friends"; Flags: nowait postinstall skipifsilent runasoriginaluser
Filename: "{app}\{#AppExe}"; Description: "Launch {#AppName} now"; Flags: nowait postinstall skipifsilent runasoriginaluser

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/f /im {#AppExe}"; Flags: runhidden; RunOnceId: "KillApp"
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall delete rule name=""Movie night"""; Flags: runhidden; RunOnceId: "DelFirewall"

[Code]
// Tailscale is what lets friends reach the stream. If it's missing, explain
// why and offer to download the official installer from tailscale.com.
const
  TailscaleURL = 'https://pkgs.tailscale.com/stable/tailscale-setup-latest.exe';

var
  TailscalePage: TWizardPage;
  InstallTailscale: TNewCheckBox;
  DownloadPage: TDownloadWizardPage;

function TailscaleInstalled: Boolean;
begin
  Result := FileExists(ExpandConstant('{commonpf64}\Tailscale\tailscale.exe')) or
            FileExists(ExpandConstant('{commonpf32}\Tailscale\tailscale.exe'));
end;

function AddText(Page: TWizardPage; Top: Integer; Caption: String; Bold: Boolean): TNewStaticText;
begin
  Result := TNewStaticText.Create(Page);
  Result.Parent := Page.Surface;
  Result.Top := Top;
  Result.Width := Page.SurfaceWidth;
  Result.WordWrap := True;
  Result.AutoSize := True;
  if Bold then
    Result.Font.Style := [fsBold];
  Result.Caption := Caption;
end;

procedure InitializeWizard;
var
  T: TNewStaticText;
begin
  TailscalePage := CreateCustomPage(wpWelcome, 'Movie night works through Tailscale',
    'Your friends need a way to reach your PC. Tailscale makes that private and simple.');

  T := AddText(TailscalePage, 0,
    'Tailscale is a free app that links your devices and your friends'' devices in a private network. ' +
    'Friends can watch your stream from anywhere, and you don''t need to open ports on your router.', False);
  T := AddText(TailscalePage, T.Top + T.Height + ScaleY(14), 'How it works:', True);
  T := AddText(TailscalePage, T.Top + T.Height + ScaleY(4),
    '1.  Install Tailscale here and sign in with Google, Microsoft, GitHub or Apple.' + #13#10 +
    '2.  On the Tailscale website, share this PC with your friends (it creates an invite link).' + #13#10 +
    '3.  Friends install Tailscale on their phone or PC and accept your invite.' + #13#10 +
    '4.  You send them Movie night''s viewer link, and they watch in their browser.', False);
  T := AddText(TailscalePage, T.Top + T.Height + ScaleY(10),
    'A step-by-step guide opens when setup finishes, and it''s always in the popcorn menu.', False);

  InstallTailscale := TNewCheckBox.Create(TailscalePage);
  InstallTailscale.Parent := TailscalePage.Surface;
  InstallTailscale.Top := T.Top + T.Height + ScaleY(18);
  InstallTailscale.Width := TailscalePage.SurfaceWidth;
  InstallTailscale.Height := ScaleY(20);
  InstallTailscale.Caption := 'Download and install Tailscale now (recommended)';
  InstallTailscale.Font.Style := [fsBold];
  InstallTailscale.Checked := True;

  DownloadPage := CreateDownloadPage('Downloading Tailscale',
    'Getting the official installer from tailscale.com...', nil);
end;

function ShouldSkipPage(PageID: Integer): Boolean;
begin
  Result := (PageID = TailscalePage.ID) and TailscaleInstalled;
end;

function UpdateReadyMemo(Space, NewLine, MemoUserInfoInfo, MemoDirInfo, MemoTypeInfo,
  MemoComponentsInfo, MemoGroupInfo, MemoTasksInfo: String): String;
begin
  Result := MemoDirInfo + NewLine + NewLine + MemoTasksInfo;
  if (not TailscaleInstalled) and InstallTailscale.Checked then
    Result := Result + NewLine + NewLine + 'Tailscale:' + NewLine + Space +
      'Download and install from tailscale.com';
end;

function NextButtonClick(CurPageID: Integer): Boolean;
var
  ResultCode: Integer;
begin
  Result := True;
  if (CurPageID <> wpReady) or TailscaleInstalled or (not InstallTailscale.Checked) then
    Exit;
  DownloadPage.Clear;
  DownloadPage.Add(TailscaleURL, 'tailscale-setup.exe', '');
  DownloadPage.Show;
  try
    try
      DownloadPage.Download;
      DownloadPage.SetText('Installing Tailscale...', 'Finish the Tailscale installer, then Movie night setup continues.');
      if not Exec(ExpandConstant('{tmp}\tailscale-setup.exe'), '', '', SW_SHOW, ewWaitUntilTerminated, ResultCode) then
        MsgBox('The Tailscale installer could not be started: ' + SysErrorMessage(ResultCode), mbError, MB_OK);
    except
      if MsgBox('Tailscale could not be downloaded:' + #13#10 + GetExceptionMessage + #13#10#13#10 +
                'Movie night will still be installed. Open tailscale.com to get Tailscale yourself?',
                mbError, MB_YESNO) = IDYES then
        ShellExec('open', 'https://tailscale.com/download/windows', '', '', SW_SHOWNORMAL, ewNoWait, ResultCode);
    end;
  finally
    DownloadPage.Hide;
  end;
end;
