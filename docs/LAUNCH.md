# Luxury Switchboard — launch kit

**Status:** repository launch materials are prepared here. The posts below have not been submitted to external platforms. Publishing source files on GitHub is not the same as publishing an article or a social post.

## Assets and article sources

- [Cover image, 1280 × 640](media/launch.png): uses the repository's fictional UI data, explicitly labelled as such.
- [Русская техническая статья для собственного блога](articles/habr.ru.md): **не отправлять на Хабр**. Его [правила Песочницы](https://habr.com/ru/sandbox/start/) исключают статьи, написанные с помощью генеративных моделей, даже если мысли авторские.
- [DEV Community: complete article](articles/dev.to.en.md): copy from the second heading, after the editorial note.
- [First request / installation](../README.md#make-your-first-request).
- [Latest installers](https://github.com/GofMan5/luxury-switchboard/releases/latest).

Read the drafts before posting in your name and adjust the voice. There are no fabricated client certifications, performance results or invented development stories in the text. Check the current platform rules, including rules about AI-assisted writing; do not auto-submit where those rules prohibit it.

## Recommended first launch

1. [r/SideProject submission](https://www.reddit.com/r/SideProject/submit): validate the explanation and installation flow. Check the current subreddit rules first; its rules API returned HTTP 403 during preparation.
2. [Show HN submission](https://news.ycombinator.com/submit): this step must remain manual. [HN guidelines](https://news.ycombinator.com/newsguidelines.html) explicitly prohibit generated post text, generated/AI-edited comments and automated posting. The browser also requires login. The facts below are research notes for the author, not a ready-to-paste submission. Write the post yourself and stay available to discuss it; also read [Show HN guidelines](https://news.ycombinator.com/showhn.html).
3. [DEV editor](https://dev.to/new): a prepared English article is included; the current browser session requires login. Check the current AI-assistance/disclosure policy before posting. Publish the Russian article on your own blog or another venue that permits AI-assisted writing. Do not send it to Habr: its [Sandbox page](https://habr.com/ru/sandbox/start/) explicitly excludes articles written with generative AI, even when the ideas belong to the author.
4. r/selfhosted only if its current rules accept this desktop-first project; r/LocalLLaMA only with a tested local-inference-server scenario. Neither is an automatic next posting target.
5. Product Hunt later, after feedback from actual users. No paid promotion, mass posting or coordinated voting is part of this launch.

A repository push does not create a Show HN, Reddit, Habr or DEV publication. Record the real public URL here only after a submission succeeds.

## Reddit / r/SideProject

**Title**

```text
I built an open-source desktop app to manage AI providers behind one local endpoint
```

**Body**

```text
I'm sharing Luxury Switchboard, an MIT-licensed desktop app for managing multiple OpenAI- and Anthropic-compatible providers.

The idea: point a compatible client at one localhost endpoint, then manage provider credentials, model aliases and failover routes in the app instead of changing every client's settings.

It includes key pools, live request activity, model reachability/latency tests, and usage/cost estimates. An optional public tunnel exposes explicitly published aliases behind an access key; it can stay off for a local-only workflow.

There is no Luxury Switchboard account or hosted control plane. You bring your own provider access; requests still go to the providers you configure and their normal charges apply.

Installers: Windows, Linux and macOS. The Windows installer is currently unsigned. Repository screenshots and the attached image use fictional demo data, not real benchmark results.

GitHub and downloads:
https://github.com/GofMan5/luxury-switchboard

The README includes a small first-request example. This is not a claim of compatibility with every editor or agent: streaming, tools and images depend on the actual client/model combination.

I'd appreciate feedback on first-run setup. Which client workflow would you want a reproducible guide for?
```

Attach [launch.png](media/launch.png) or [the Model Routes screenshot](screenshots/routes.png). This is an image post with an explanation, not a live benchmark or a recorded end-to-end demo.

## Hacker News / Show HN

**Submission notes (write the post yourself)**

HN's guidelines say to submit the original source, avoid promotional framing, and omit the site's name from the title because it is shown after the link. Do not reuse a generated title. Choose a factual title in your own words after reading the repository and link directly to:

```text
https://github.com/GofMan5/luxury-switchboard
```

**Facts to verify before writing your own introductory comment**

- Desktop-first, open source under MIT; Go relay/backend, Tauri v2 lifecycle bridge, React UI.
- A stable loopback endpoint with provider credentials, aliases, key pools and ordered failover managed locally.
- Request activity, model tests and usage/cost estimates; optional separately published tunnel routes.
- No hosted Luxury Switchboard control plane; bring your own provider access. Remote upstreams still receive their requests.
- Windows/Linux/macOS installers. Windows is not Authenticode-signed. Release assets include checksums, not an independent publisher attestation.
- Useful feedback: first-run setup and reproducible API/client compatibility issues. Do not claim measured performance or endorse an untested client.

Do not paste generated text into a discussion if the site's current guidelines disallow it. Do not ask friends to upvote or comment. A launch requires an author available to discuss the work; it is not an unattended advertising job.

## Короткий пост для Telegram

```text
Сделал Luxury Switchboard — open-source desktop-приложение, чтобы не перенастраивать каждый AI-клиент при смене провайдера.

Клиент обращается к одному localhost endpoint. В приложении — провайдеры, ключи, aliases моделей, failover и история статусов запросов. Есть оценки расходов и тесты моделей.

Windows / Linux / macOS, лицензия MIT. Без аккаунта Luxury Switchboard и нашего облачного control plane. Свой доступ к API нужен: запросы идут к выбранным провайдерам и оплачиваются по их условиям.

GitHub и установщики:
https://github.com/GofMan5/luxury-switchboard

На картинке — демонстрационные данные, не результаты benchmark. Буду рад воспроизводимым багам и отзывам о первом запуске. Ключи и личную переписку в issues не прикладывайте.
```

Сначала согласуйте пост с администраторами чата, если самостоятельные анонсы не разрешены. Не отправляйте массовые личные сообщения.

## Short post for X / Bluesky

```text
Luxury Switchboard: an MIT-licensed desktop app for your AI providers.
One local endpoint, model routes, key pools and request activity.
Windows / Linux / macOS. Bring your own provider access.
https://github.com/GofMan5/luxury-switchboard
```

Attach the cover separately; it is labelled as demo data. Do not add fabricated download counts, user testimonials, or performance claims.

## Reply facts

- **Free models?** No. Users connect their own accounts/endpoints and pay the provider.
- **Offline?** No. The control plane is local, but providers receive requests; updates, sign-in and the optional tunnel have their own external services.
- **Local inference?** This app is a relay, not a model runner. A compatible loopback server can be an upstream.
- **Every client?** No certification claim. Test the real dialect, tools, images, streaming and cancellation.
- **Same model after failover?** Not necessarily. Aliases can map to different models with different capabilities and prices.
- **Guaranteed safety?** No. Guardrails default to monitor mode and can miss or falsely flag content.
- **Hosted multi-user gateway?** Desktop-first. Evaluate server identity, isolation and operations requirements separately.
- **Bug report?** App version, OS, client, API dialect, minimal fictional reproduction. Never keys, session JSON, exported backups or private prompts.

## Evaluate the launch without adding telemetry

Use GitHub's existing repository traffic and aggregate release download counters, plus voluntary issues and discussions. Do not add tracking SDKs or inspect users' API traffic for marketing. Downloads are not unique users or proof of successful installation.

First milestone: ten voluntary reports of a successful documented workflow. Stars are a secondary discovery signal, not a promised outcome.
