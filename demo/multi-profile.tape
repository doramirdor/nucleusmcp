# multi-profile.tape — showcases two Supabase profiles side-by-side,
# the ".mcp-profiles.toml" binding, and the namespaced tool names
# produced by the router.
#
# Regenerate with:  vhs demo/multi-profile.tape

Output demo/multi-profile.gif

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 820
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms

Type "# two real Supabase profiles, one workspace"
Sleep 1.2s
Enter
Sleep 400ms

Type "nucleusmcp list"
Enter
Sleep 3s

# Write a .mcp-profiles.toml that exposes both with friendly aliases
Type "cat > .mcp-profiles.toml <<EOF"
Enter
Type "[[supabase]]"
Enter
Type 'profile = "atlas"'
Enter
Type 'alias   = "prod"'
Enter
Type 'note    = "PRODUCTION — read-only unless explicitly asked"'
Enter
Type ""
Enter
Type "[[supabase]]"
Enter
Type 'profile = "default"'
Enter
Type 'alias   = "staging"'
Enter
Type 'note    = "staging"'
Enter
Type "EOF"
Enter
Sleep 1s

Type "cat .mcp-profiles.toml"
Enter
Sleep 3s

# The resolver's view, printed as log lines on startup
Type "# starting the gateway prints which profile each alias binds to:"
Enter
Sleep 600ms
Type "echo '' | nucleusmcp serve 2>&1 | grep 'resolved profile' | head"
Enter
Sleep 4s

Sleep 1.5s
