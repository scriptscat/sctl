# Browser data

Contents: [tabs, windows, groups](#tabs-windows-and-tab-groups) · [bookmarks, reading list](#bookmarks-and-reading-list) ·
[history, recent, downloads](#history-recently-closed-and-downloads) · [cookies, browsing data](#cookies-and-browsing-data) ·
[extensions](#extensions) · [browser instances](#browser-instances)

Every group takes `--browser <name|ID prefix>`. Read results with `-o json`. Lists default to `--limit 100`
(1–1000).

**Multi-item commands are all-or-nothing:** `tabs move/pin/mute/reload`, `windows close`, `bookmarks move` and
the like change every item or none.

Time flags (`--since`, `--until`) take an RFC 3339 time or a duration ago such as `7d`, `12h`, `30m`.

## Tabs, windows and tab groups

| Command | Notes |
|---|---|
| `tabs list [--window W]` | tabId, windowId, active, pinned, groupId, title, url |
| `tabs open <url> [--background] [--window W]` | Opens a tab and prints its ID. Activates it unless `--background` — prefer `--background` so the user's current tab stays in front |
| `tabs close <tabId>...` | Closes tabs (`recent restore` brings one back) |
| `tabs activate <tabId>` | Activates the tab and focuses its window |
| `tabs move <tabId>... [--window W] [--index I]` | Moves to a window and position; `-1` is the end |
| `tabs pin/unpin/mute/unmute <tabId>...` | Pin and mute state |
| `tabs reload <tabId>... [--bypass-cache]` | Reloads |
| `tabs duplicate <tabId>` | Duplicates and prints the new tab ID |
| `windows list` / `windows open [<url>...] [--state S]` / `windows close <id>...` / `windows focus <id>` / `windows state <id> normal\|minimized\|maximized\|fullscreen` | Windows |
| `groups list [--window W]` / `groups create <tabId>... [--title T] [--color C]` / `groups add <groupId> <tabId>...` / `groups edit <groupId> [--title] [--color] [--collapse\|--expand]` / `groups ungroup <tabId>...` | Tab groups. Colors: grey, blue, red, yellow, green, pink, purple, cyan, orange |

## Bookmarks and reading list

| Command | Notes |
|---|---|
| `bookmarks list [--folder ID] [--recursive]` | Children of a folder; the top-level folders by default |
| `bookmarks search <query>` | Matches title and URL, shows each result's folder path |
| `bookmarks add <url> [--title] [--folder ID] [--index I]` | Goes to "Other bookmarks" by default |
| `bookmarks mkdir <title> [--folder ID]` | Creates a folder |
| `bookmarks move <id>... --folder ID [--index I]` | Moves bookmarks or folders |
| `bookmarks edit <id> [--title] [--url]` | Changes a title or URL |
| `bookmarks rm <id>...` | Deletes bookmarks and folders with everything inside. **Blocks until the user approves in the browser** |
| `reading-list list [--read\|--unread]` / `add <url> [--title]` / `mark-read <url>... [--unread]` / `rm <url>... --yes` | Reading list |

## History, recently closed and downloads

| Command | Notes |
|---|---|
| `history search [text] [--since] [--until]` | Newest first |
| `history visits <url>` | Every visit of one URL |
| `history rm <url>... --yes` | Deletes **every** visit of those URLs, whatever the date. There is no "this site, this period" delete: to remove a site's recent history, `history search <text> --since …`, keep the URLs that really belong to the site, tell the user older visits of the same URLs go too, then `history rm` them |
| `history clear [--since] [--until] --yes` | Clears the history of **all sites** in the range, and all history with no range: agree on the range with the user first |
| `recent list` / `recent restore [<sessionId>]` | Recently closed tabs and windows (Chrome keeps at most 25); without an ID restores the latest |
| `downloads list [--state in_progress\|complete\|interrupted] [--query text]` | Download list |
| `downloads start <url> [--filename relative/path]` | Downloads into the browser's default directory; never overwrites |
| `downloads pause/resume/show <id>` | Pause, resume; `show` reveals the file in the system file manager |
| `downloads cancel <id> --yes` / `erase <id>... --yes` / `delete-file <id> --yes` | Cancel a download; `erase` removes only the list entry; `delete-file` removes the file from disk |

## Cookies and browsing data

| Command | Notes |
|---|---|
| `cookies list [--url U\|--domain D] [--name N]` | Includes partitioned cookies. The output includes cookie values. With no filter it lists every site: filter whenever you can |
| `cookies get --url U --name N` | Exit code 3 when the cookie does not exist |
| `cookies set --url U --name N --value V [--domain] [--path] [--expires RFC3339] [--secure] [--http-only] [--same-site lax\|strict\|no_restriction]` | A session cookie unless `--expires` is given |
| `cookies rm --url U --name N --yes` | Deletes one cookie |
| `cookies clear (--domain D \| --all) --yes` | Deletes a domain's cookies (subdomains included) or every cookie |
| `browsing-data clear --types t1,t2 [--since] [--origin https://x]... --yes` | Types: cache, cacheStorage, cookies, downloads, fileSystems, formData, history, indexedDB, localStorage, serviceWorkers, webSQL. `--origin` works only with cache, cacheStorage, cookies, fileSystems, indexedDB, localStorage, serviceWorkers and webSQL. Passwords are not managed |

Cookie values are session credentials. Show them only when the user needs them, and keep them out of logs and
files.

## Extensions

| Command | Notes |
|---|---|
| `extensions list` | Installed extensions and apps |
| `extensions enable <id>` | Enables one |
| `extensions disable <id> --yes` | sctl Browser cannot disable itself or policy-installed extensions. Disabling ScriptCat disconnects it from the daemon |
| `extensions uninstall <id>` | **Needs the user twice:** approval in sctl Browser's window, then Chrome's own confirmation dialog |

## Browser instances

| Command | Notes |
|---|---|
| `browsers` / `browsers list` | Paired instances: name, ID, online, product and version |
| `browsers forget <name\|id>` | Deletes the pairing key and disconnects the instance; it must be paired again afterwards. Only when the user asks |
