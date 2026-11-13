# Mitwirken

Danke, dass du Phoedox verbessern willst! Phoedox besteht aus mehreren Repositories:
[phoedox](https://github.com/phoedox/phoedox) (Server + Web), [phoedox-sdk](https://github.com/phoedox/phoedox-sdk),
[phoedox-plugins](https://github.com/phoedox/phoedox-plugins), [phoedox-agent](https://github.com/phoedox/phoedox-agent),
[phoedox-store](https://github.com/phoedox/phoedox-store) und [phoedox-app](https://github.com/phoedox/phoedox-app).
Die grundlegenden Entscheidungen stehen in [`phoedox/docs/decisions.md`](https://github.com/phoedox/phoedox/blob/main/docs/decisions.md).

## Bevor du loslegst

- Für größere Änderungen bitte zuerst ein Issue öffnen und den Ansatz abstimmen.
- Sicherheitslücken **nicht** als Issue melden, sondern wie in [SECURITY.md](SECURITY.md) beschrieben.

## Ablauf

1. Fork, Branch von `main` (`feat/…`, `fix/…`).
2. Änderungen mit Tests. Befehle stehen in der README bzw. `CLAUDE.md` des Repos.
3. Commits nach [Conventional Commits](https://www.conventionalcommits.org/de/) (`feat:`, `fix:`, `docs:` …) – daraus entstehen Versionen und Changelog.
4. Pull Request mit ausgefüllter Vorlage.

## Contributor License Agreement (CLA)

Für Beiträge zu Phoedox ist ein CLA nötig. Damit räumst du dem Projekt die Rechte ein, deinen Beitrag
auch unter anderen Lizenzen anzubieten (z. B. für eine Enterprise-Lizenz neben der AGPL). Du behältst dein Urheberrecht.
Beim ersten Pull Request fordert dich der CLA-Bot zur Zustimmung auf.

> Der CLA-Text wird gerade rechtlich geprüft. Bis dahin können Pull Requests nicht gemergt werden.

## Stil

- Code, Bezeichner und Kommentare auf Englisch; Oberflächentexte Deutsch und Englisch über i18n.
- Icons ausschließlich aus Lucide.
- Keine Geheimnisse, Tokens oder echten Kundendaten in Code, Tests oder Screenshots.
