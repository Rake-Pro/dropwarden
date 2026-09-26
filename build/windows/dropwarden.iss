; dropwarden per-user installer. Built by .github/workflows/release.yml:
;   iscc /DAppVersion=<ver> /DBinDir=<abs path to built exes> /O<outdir> dropwarden.iss
; Per-user install (no admin): {autopf} + PrivilegesRequired=lowest resolves to
; %LOCALAPPDATA%\Programs\dropwarden, which also lets the in-app updater swap
; the exes without elevation. dropwarden-cli.exe ships next to the GUI: it is
; the updater's out-of-process installer and the headless CLI.

#ifndef AppVersion
  #define AppVersion "0.0.0"
#endif
#ifndef BinDir
  #define BinDir "..\..\dist\windows"
#endif

[Setup]
AppId={{F3E61A3D-3F07-416B-A214-18437844DC06}
AppName=dropwarden
AppVersion={#AppVersion}
AppPublisher=Rake-Pro
DefaultDirName={autopf}\dropwarden
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
OutputBaseFilename=dropwarden_{#AppVersion}_windows_amd64_setup
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
SetupIconFile=..\icons\dropwarden.ico
UninstallDisplayIcon={app}\dropwarden.exe
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes

[Tasks]
Name: "startup"; Description: "Start dropwarden when you log in"; Flags: unchecked

[Files]
Source: "{#BinDir}\dropwarden.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#BinDir}\dropwarden-cli.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\dropwarden"; Filename: "{app}\dropwarden.exe"
Name: "{userstartup}\dropwarden"; Filename: "{app}\dropwarden.exe"; Tasks: startup

[Run]
Filename: "{app}\dropwarden.exe"; Description: "Launch dropwarden"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
; Executables the in-app updater moved aside (removed on the next start
; normally; this catches an uninstall before that).
Type: files; Name: "{app}\*.old"
; Abandoned update stage and probe directories.
Type: filesandordirs; Name: "{app}\.dropwarden-update-*"

[Code]
// Smart App Control (Windows 11 clean installs) blocks unsigned binaries with
// no per-app exception. This installer is not code-signed, so when SAC is in
// evaluation mode (or, rarely, On and this build slipped through) tell the
// user up front how to turn it off. In the plain blocked case this installer
// never runs; the README repeats the steps.
function InitializeSetup(): Boolean;
var
  State: Cardinal;
  Msg: String;
  ErrorCode: Integer;
begin
  Result := True;
  if not RegQueryDWordValue(HKLM, 'SYSTEM\CurrentControlSet\Control\CI\Policy',
      'VerifiedAndReputablePolicyState', State) then
    Exit;
  if State = 0 then
    Exit;
  if State = 2 then
    Msg := 'Smart App Control is in evaluation mode on this PC. Once Windows switches it on, it will block this copy of dropwarden and its updates.'
  else
    Msg := 'Smart App Control is turned on on this PC. It will block this copy of dropwarden and its updates.';
  Msg := Msg + ' This download is not code-signed, and Smart App Control has no per-app exception.' + #13#10#13#10 +
    'To run dropwarden, turn Smart App Control off: Windows Security > App & browser control > ' +
    'Smart App Control settings > Off.' + #13#10#13#10 +
    'Yes = open Windows Security and continue setup' + #13#10 +
    'No = continue setup without opening it';
  if MsgBox(Msg, mbConfirmation, MB_YESNO) = IDYES then
    ShellExec('open', 'windowsdefender://appbrowser', '', '', SW_SHOWNORMAL, ewNoWait, ErrorCode);
end;
