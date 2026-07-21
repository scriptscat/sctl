# 文档维护

## 一个事实只有一个归属文档

每条事实(某个端口、某条退出码、某个版本号)只在**一份**文档里展开,
其余地方交叉链接过去。复制到两处的事实一定会漂移,而且通常要等到有人照着错的那份做事才暴露。
归属表见 [README.md](./README.md)。

## 真实性纪律

**当前分支上 `git grep` 不到的东西,就不要写进文档。**

验证一律用 git 感知的命令:

```bash
git grep -n "someSymbol"          # 而不是 rg
git ls-files 'internal/**/*.go'   # 而不是 ls / find
git ls-tree -r --name-only HEAD
```

`rg` / `ls` / `find` 会连**未跟踪**的本地文件一起匹配,于是还没合入的东西看上去像是已经发布了——
这是文档说谎最常见的来源。

所有数量词(「6 个交叉编译目标」「6 个 MCP 工具」)都要从权威源头数出来,不能凭记忆或抄前文。

## 发现不一致时

以**当前分支的代码**为准,改文档去对齐代码。除非代码确实是错的——那就改代码,并在说明里讲清楚。

过期内容**直接删掉**,不要在上面叠加更正。一段「注:上面那句已不适用」只会让下一个读者两句都不敢信。

改名 / 挪文件时,索引与所有引用它的文档在**同一次改动**里一起改完。

## 落地前跑一遍

改动任何 Markdown 之前(或之后)整段贴进终端跑一遍,只关心它有没有输出:

```bash
set -u
fail=0

# 1) 相对链接不能断,且指向的必须是被 git 跟踪的文件。
#    用 git -C <文档所在目录> 解析,既跨平台又天然只认已跟踪文件。
git ls-files '*.md' | while read -r f; do
  dir=$(dirname "$f")
  grep -oE '\]\(\.[^)]*\)' "$f" | sed -E 's/^\]\(//; s/\)$//; s/#.*$//' | while read -r link; do
    [ -z "$link" ] && continue
    git -C "$dir" ls-files --error-unmatch "$link" >/dev/null 2>&1 || echo "断链:$f -> $link"
  done
done

# 2) golangci-lint 版本:CI 与开发文档必须是同一个。
ci_ver=$(grep -oE 'GOLANGCI_LINT_VERSION: v[0-9.]+' .github/workflows/test.yaml | grep -oE 'v[0-9.]+')
doc_ver=$(grep -oE 'golangci-lint@v[0-9.]+' docs/development.md | grep -oE 'v[0-9.]+' | sort -u)
[ "$ci_ver" = "$doc_ver" ] || echo "golangci-lint 版本漂移:CI=$ci_ver 文档=$doc_ver"

# 3) 版本门槛:文档里的数字必须等于 protocol.json 的权威值。
proto_min=$(grep -oE '"minDaemonVersion": *"[^"]*"' internal/pkg/protocol/protocol.json | grep -oE '[0-9]+\.[0-9]+\.[0-9]+')
grep -q "当前 \`$proto_min\`" docs/development.md || echo "minDaemonVersion 漂移:protocol.json=$proto_min"

# 4) 文档里出现的 internal/ 路径必须真的被跟踪。
git grep -ohE '\binternal/[A-Za-z0-9_/.-]+\.go\b' -- '*.md' | sort -u | while read -r p; do
  git ls-files --error-unmatch "$p" >/dev/null 2>&1 || echo "文档引用了不存在的源文件:$p"
done

# 5) 反向:代码与 CI 注释里引用的文档也必须真的在仓库里。
#    指向仓库外设计文档、或早已改名的根级 PROTOCOL.md / THREAT-MODEL.md 的「§X」引用,读者无从查证。
git grep -nE '设计文档|PROTOCOL\.md|THREAT-MODEL|PROTOCOL §' -- '*.go' '*.yaml' '*.yml'
git grep -ohE '\bdocs/[A-Za-z0-9_-]+\.md\b' -- '*.go' '*.yaml' '*.yml' | sort -u | while read -r p; do
  git ls-files --error-unmatch "$p" >/dev/null 2>&1 || echo "注释引用了不存在的文档:$p"
done

echo "文档校验结束(以上无输出即通过)"
```

新增一类具体声明(又一个版本号、又一份镜像文件)时,给这个块补一条对应的检查——
校验块覆盖不到的声明,迟早会悄悄说谎。
