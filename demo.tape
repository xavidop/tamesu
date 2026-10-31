# Records site/public/demo.gif. Run `make demo` (it builds ./bin/tamesu and
# seeds /tmp/tamesu-demo first).
Output site/public/demo.gif

Require ./bin/tamesu

Set Shell "bash"
Set FontSize 20
Set Width 1100
Set Height 600
Set Framerate 15
Set Padding 12
Set Theme "Catppuccin Mocha"
Set TypingSpeed 50ms

Env PS1 "$ "
Env GEMINI_API_KEY "demo-gemini-key"
Env SUPPORT_API_TOKEN "demo-support-token"
Env LIVEKIT_API_KEY "demo-livekit-key"
Env LIVEKIT_API_SECRET "demo-livekit-secret"

Hide
Type "cd /tmp/tamesu-demo && export PATH=$PWD/bin:$PATH && clear"
Enter
Show

Type "tamesu init"
Enter
Sleep 2.5s

Type "tamesu validate"
Enter
Sleep 2.5s

Type "tamesu list"
Enter
Sleep 3s

Type "tamesu doctor"
Enter
Sleep 4s
