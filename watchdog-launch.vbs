' workbuddy2api silent launcher for Windows scheduled tasks.
' Purpose: scheduled task action points at wscript.exe, which relays to a .ps1.
' Reason: on Windows 11 with Windows Terminal as the default terminal app,
'         Task Scheduler launching powershell.exe directly flashes a console
'         window (~0.4s) even with -WindowStyle Hidden, once per minute.
'         wscript.exe is a GUI-subsystem host, so Run(..., 0, True) starts the
'         PowerShell child fully hidden and waits for it.
' Semantics preserved: the third arg True waits, so the task's IgnoreNew
'         policy still prevents overlapping runs; the exit code is returned
'         unchanged so LastTaskResult stays meaningful.
' NOTE: keep this file pure ASCII. WScript reads .vbs using the system ANSI
'       code page, so non-ASCII bytes can corrupt the script when the code
'       page does not match the file encoding.
'
' Usage: wscript.exe watchdog-launch.vbs <target.ps1> [extra arg ...]
'        With no argument it falls back to watchdog.ps1 next to this file.

Option Explicit

Dim sh, fso, here, ps, script, arg, cmd, rc, i

Set sh = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")

ps = "C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe"

' Resolve the target script so the task's working directory does not matter.
here = fso.GetParentFolderName(WScript.ScriptFullName)

If WScript.Arguments.Count >= 1 Then
    script = WScript.Arguments(0)
    If InStr(script, "\") = 0 Then script = fso.BuildPath(here, script)
Else
    script = fso.BuildPath(here, "watchdog.ps1")
End If

If Not fso.FileExists(script) Then
    WScript.Echo "Target script not found: " & script
    WScript.Quit 1
End If

cmd = """" & ps & """ -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File """ & script & """"

' Pass through any remaining arguments (quoted).
For i = 1 To WScript.Arguments.Count - 1
    arg = WScript.Arguments(i)
    If InStr(arg, " ") > 0 Then arg = """" & arg & """"
    cmd = cmd & " " & arg
Next

On Error Resume Next
rc = sh.Run(cmd, 0, True)
If Err.Number <> 0 Then
    WScript.Quit 1
End If
On Error GoTo 0

WScript.Quit rc
