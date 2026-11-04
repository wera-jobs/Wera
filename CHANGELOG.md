# Changelog

## [0.15.0](https://github.com/wera-jobs/Wera/compare/wera-v0.14.0...wera-v0.15.0) (2026-11-04)


### Features

* **api:** take a job off the tracker ([078db86](https://github.com/wera-jobs/Wera/commit/078db866ad23e2f3477613bf21c6c4ce6b70f5c7))


### Bug Fixes

* "Applied this week" counted every application row touched in the ([0d02f19](https://github.com/wera-jobs/Wera/commit/0d02f19ceaaa439056de3325a177ddfd2d5c1b98))
* **scoring:** write fit reasons to the user as "you" ([7fe9e08](https://github.com/wera-jobs/Wera/commit/7fe9e08fea850f9719c74108402fb892ed77e112))


### Performance

* **api:** count industries, companies and stats from live matches only ([0d02f19](https://github.com/wera-jobs/Wera/commit/0d02f19ceaaa439056de3325a177ddfd2d5c1b98))
* **store:** index postings by company and matches by stage ([041c474](https://github.com/wera-jobs/Wera/commit/041c4743b86fa6e0601a8efa5a9f4eb9b6f45e63))

## [0.14.0](https://github.com/wera-jobs/Wera/compare/wera-v0.13.0...wera-v0.14.0) (2026-11-04)


### Features

* **api:** filter and sort jobs by when they were posted ([a36332a](https://github.com/wera-jobs/Wera/commit/a36332a09ab0c9d8cf67177aa6f8742d99c7d73f))
* **api:** keep the posting date of jobs added from a page ([c574b2b](https://github.com/wera-jobs/Wera/commit/c574b2bca7277263722647af1e94116ccab20b8d))
* **pipeline:** close postings of boards unread for a week ([4725f61](https://github.com/wera-jobs/Wera/commit/4725f61e619730f975e389205c4023b738672772))
* **scoring:** score fresh postings before months-old ones ([eef2e81](https://github.com/wera-jobs/Wera/commit/eef2e81c49c167c093cf4b2e3a3401c03d930288))


### Documentation

* explain the run schedule and how stale postings are handled ([fcccdf5](https://github.com/wera-jobs/Wera/commit/fcccdf5d16f26bb8b0e72d8dbf6c75f3c13d7bf8))

## [0.13.0](https://github.com/wera-jobs/Wera/compare/wera-v0.12.0...wera-v0.13.0) (2026-11-04)


### Features

* **ext:** match a job page's keywords against the resume ([41776c3](https://github.com/wera-jobs/Wera/commit/41776c3907c61db852159da1999b93104965ac82))
* **resumes:** show the fit score a tailored resume earns ([540e07c](https://github.com/wera-jobs/Wera/commit/540e07caec7c9a45df8815b78ff65aa143188ef6))
* **worker:** run on chosen weekdays with RUN_DAYS ([d11c59c](https://github.com/wera-jobs/Wera/commit/d11c59c56510b63693393990ea5d38d3c1ccda96))


### Bug Fixes

* **deps:** bump github.com/jackc/pgx/v5 ([#108](https://github.com/wera-jobs/Wera/issues/108)) ([ef3f873](https://github.com/wera-jobs/Wera/commit/ef3f873a7f1920bb1a866715fc1effc4229121c6))

## [0.12.0](https://github.com/wera-jobs/Wera/compare/wera-v0.11.0...wera-v0.12.0) (2026-11-03)


### Features

* **mail:** send digests of new matches after pipeline runs ([dbdfe5e](https://github.com/wera-jobs/Wera/commit/dbdfe5e58c7c485e90da286593abe691ab29c0bb))
* **mail:** welcome accounts when they confirm their address ([2fb82b7](https://github.com/wera-jobs/Wera/commit/2fb82b7332d20a7258f089825930d7101c3250d2))


### Bug Fixes

* **fetch:** name wera-jobs.com in the User-Agent ([a2dc957](https://github.com/wera-jobs/Wera/commit/a2dc957c893d0dee6616ecd3e8935db6d31fed9b))
* **privacy:** remove email and AI notes with the account ([76e4c3a](https://github.com/wera-jobs/Wera/commit/76e4c3af069a67c341520bc51bfd1b1856739555))
* **rank:** spend no inactive scores on digests that cannot arrive ([96f6189](https://github.com/wera-jobs/Wera/commit/96f6189fc7518e2c88563ec48855dbad4e3de6d5))


### Documentation

* **readme:** describe Wera as the service it is today ([0499167](https://github.com/wera-jobs/Wera/commit/049916794f8ca251063000104fd73d4888a7f0eb))

## [0.11.0](https://github.com/wera-jobs/Wera/compare/wera-v0.10.0...wera-v0.11.0) (2026-11-01)


### Features

* **deploy:** run the public site on a named tunnel ([e7a7d5d](https://github.com/wera-jobs/Wera/commit/e7a7d5d3f0947c7d545688ae50f4dd229a2c724c))


### Documentation

* **readme:** point links at the wera-jobs org ([8253bc6](https://github.com/wera-jobs/Wera/commit/8253bc65042a8c61baaa07e9964ae2d0acf7fe87))

## [0.10.0](https://github.com/jessechumo/Wera/compare/wera-v0.9.1...wera-v0.10.0) (2026-10-31)


### Features

* **rank:** learn each user's fit from the AI's own scores ([27c1aaf](https://github.com/jessechumo/Wera/commit/27c1aafee6de830acd1135c155cbb62ff99c3738))
* **rank:** rank on what the fit score weighs ([0dc26c9](https://github.com/jessechumo/Wera/commit/0dc26c929c739fb5818e30cda3dc85bf92aa9842))


### Bug Fixes

* **companies:** follow five boards that moved and stop fetching five that are gone ([ab1e106](https://github.com/jessechumo/Wera/commit/ab1e1067c803c4608435303c891b22ccb04553a2))
* **rank:** keep the best jobs in order when many share an estimate ([2139dda](https://github.com/jessechumo/Wera/commit/2139ddac0c62e8de9d6a04344809741e2ca247ee))


### Documentation

* **home:** keep the Wi-Fi finding to what the logs show ([8fdf138](https://github.com/jessechumo/Wera/commit/8fdf1384ceb52eab54b0d549917ebcde0b630cd2))
* **home:** say that the server's Wi-Fi caused the long outages ([0584680](https://github.com/jessechumo/Wera/commit/0584680e62ff6f7acf3513cec74c9e50ee0acc9f))
* **readme:** describe how ranking learns from the AI's scores ([14655e7](https://github.com/jessechumo/Wera/commit/14655e7656fa50d555454fa7c23d4ba9e9df19b8))

## [0.9.1](https://github.com/jessechumo/Wera/compare/wera-v0.9.0...wera-v0.9.1) (2026-10-31)


### Bug Fixes

* **api:** never cut a character in half when shortening text ([8493c0b](https://github.com/jessechumo/Wera/commit/8493c0bf0caae86a9e30a8a1e9054548e0112ac9))
* **autofill:** never give a confident no that the data does not state ([6b1d292](https://github.com/jessechumo/Wera/commit/6b1d2924795a44bd1fc552c6ea2515a88b8a42a9))
* **deploy:** bring the public site back after a long network outage ([#82](https://github.com/jessechumo/Wera/issues/82)) ([7d890fa](https://github.com/jessechumo/Wera/commit/7d890fa30bf769acaae1e7209256bda0910ccab7))
* **fetch:** save postings with text Postgres refuses ([dd32b42](https://github.com/jessechumo/Wera/commit/dd32b42eac4150f79aa636a82c21b8acaa61a688))
* **fetch:** stop reading a company's name as a maintenance page ([29d7add](https://github.com/jessechumo/Wera/commit/29d7adde0168adb65553fd95de1dc4725d4179cf))
* **fetch:** stop reading a job's title as a maintenance page, and say why details fail ([6c8c853](https://github.com/jessechumo/Wera/commit/6c8c8530894525072b24c1b50048f47c0bc2ec21))
* **jobs:** keep added jobs open when their company is fetched ([58c8f45](https://github.com/jessechumo/Wera/commit/58c8f45d5ce780cb615945910771f72f5ce31603))
* release the outage recovery, the fetch fixes and the tracker fix ([b4b2721](https://github.com/jessechumo/Wera/commit/b4b2721992d275dfa28e16276e5e76936771c5c4))
* **resume:** count bullet suggestions against the user's AI budget ([944b7c0](https://github.com/jessechumo/Wera/commit/944b7c04d94f9dea0e64b015267f7f475126ba4b))
* **scoring:** stop a batch when the AI provider keeps failing ([b1d5a59](https://github.com/jessechumo/Wera/commit/b1d5a59951ef048ea96eeb185f12eec71de882c0))
* **serve:** schedule a catch-up run only after a run that happened ([bf8fe08](https://github.com/jessechumo/Wera/commit/bf8fe08e58be78661ee29e7e7d6a5f61f27c6119))
* **tracker:** keep applications whose posting has closed ([1e10009](https://github.com/jessechumo/Wera/commit/1e1000916cc8cbee8ec6778fe69a1296aa0c3557))


### Documentation

* **contributing:** say pull requests go to dev first ([4dcb813](https://github.com/jessechumo/Wera/commit/4dcb813e72f09d88bed7101d25826771be6c0629))
* **readme:** describe the home server, backups and what to do when things break ([ce0a097](https://github.com/jessechumo/Wera/commit/ce0a097d15faf803e443a9c83902e70b5fec958c))

## [0.9.0](https://github.com/jessechumo/Wera/compare/wera-v0.8.2...wera-v0.9.0) (2026-10-31)


### Features

* **scoring:** take traps for AI tools out of what models read ([54084f0](https://github.com/jessechumo/Wera/commit/54084f0e3963545755f49f0a97c4431c39c875b1))
* **scoring:** take traps for AI tools out of what models read ([#78](https://github.com/jessechumo/Wera/issues/78)) ([bb94da4](https://github.com/jessechumo/Wera/commit/bb94da4bed3b2ec82f4e6b7ae78f892cd44198b2))


### Documentation

* **security:** say how traps for AI tools are handled ([#79](https://github.com/jessechumo/Wera/issues/79)) ([3847eb5](https://github.com/jessechumo/Wera/commit/3847eb5b4b8d27b333a43853a6bcb06bc268dec6))

## [0.8.2](https://github.com/jessechumo/Wera/compare/wera-v0.8.1...wera-v0.8.2) (2026-10-28)


### Bug Fixes

* **scoring:** score from 0-10 ratings of each part of fit, rescore old scores in place ([#75](https://github.com/jessechumo/Wera/issues/75)) ([56f3742](https://github.com/jessechumo/Wera/commit/56f3742dca772015282f3a53e89712b13384a331))

## [0.8.1](https://github.com/jessechumo/Wera/compare/wera-v0.8.0...wera-v0.8.1) (2026-10-24)


### Bug Fixes

* **api:** recover panics in background tasks instead of crashing the server ([#72](https://github.com/jessechumo/Wera/issues/72)) ([a7afcdb](https://github.com/jessechumo/Wera/commit/a7afcdb0f7f78fef71e28775922f684bd74ec18e))

## [0.8.0](https://github.com/jessechumo/Wera/compare/wera-v0.7.2...wera-v0.8.0) (2026-10-22)


### Features

* **ai:** rework answers on request, from the user's own stories ([c5359db](https://github.com/jessechumo/Wera/commit/c5359db9e5997c480798d05dac5350e65e94f2a7))
* **ai:** rework answers on request, from the user's own stories ([bb29129](https://github.com/jessechumo/Wera/commit/bb291292d4dd8adcce86ec6e9ae8bc5494b4b326))

## [0.7.2](https://github.com/jessechumo/Wera/compare/wera-v0.7.1...wera-v0.7.2) (2026-10-22)


### Bug Fixes

* **ai:** tell form answers today's date ([18927e4](https://github.com/jessechumo/Wera/commit/18927e4c8ba476b6e6f47f16240266aee97749fd))
* **ai:** tell form answers today's date and drop start dates that have passed ([61d08f7](https://github.com/jessechumo/Wera/commit/61d08f714ec67b9a9c88d68077a71b77e196e10b))

## [0.7.1](https://github.com/jessechumo/Wera/compare/wera-v0.7.0...wera-v0.7.1) (2026-10-22)


### Bug Fixes

* **api:** reading application details never starts an AI import ([170fc5e](https://github.com/jessechumo/Wera/commit/170fc5e51e0af4fbfa5ca73968e35fafa337fe8d))
* **api:** self-identification starts unanswered, never declined ([54b45fe](https://github.com/jessechumo/Wera/commit/54b45fe4348862d7c0bb4a13f778ebe3a6d78499))

## [0.7.0](https://github.com/jessechumo/Wera/compare/wera-v0.6.0...wera-v0.7.0) (2026-10-22)


### Features

* **api:** fill application details from the profile and resume ([0ae9ca5](https://github.com/jessechumo/Wera/commit/0ae9ca5fe22bd32541017e9111255df47a3b14f5))
* **resume:** read a resume's jobs, schools and contact details ([43b84bd](https://github.com/jessechumo/Wera/commit/43b84bd70f050682c0da8fd25f1453ea4dcd99c1))

## [0.6.0](https://github.com/jessechumo/Wera/compare/wera-v0.5.0...wera-v0.6.0) (2026-10-21)


### Features

* **api:** cover letter as a PDF file ([8b18fb5](https://github.com/jessechumo/Wera/commit/8b18fb5817b044ac62e7dd5cd8af083fd58d8022))
* **api:** show which jobs the extension filled ([2e52cba](https://github.com/jessechumo/Wera/commit/2e52cba9539eec4d25f97517819a3c0cab22160d))
* better job ranking: Arctic Embed M plus a blended score ([8a6733e](https://github.com/jessechumo/Wera/commit/8a6733e19de77b6ad60b5e56ed526e3396c90e53))
* **embed:** give the model the query and document prefixes it expects ([cb359ba](https://github.com/jessechumo/Wera/commit/cb359ba0e80afdd44284029a52d8cacbdf9c41a3))
* **embed:** rank with Snowflake Arctic Embed M v1.5 ([928406f](https://github.com/jessechumo/Wera/commit/928406f71fbc52ea1b45923094e64c92eafbf9b8))
* **pipeline:** rank jobs by a blend of embeddings, words, skills and years ([dd4cd00](https://github.com/jessechumo/Wera/commit/dd4cd00d723b6a838b7a2bee8a76269bb85edb30))
* **relevance:** add similarity, skill-overlap and standardize helpers ([7ed9564](https://github.com/jessechumo/Wera/commit/7ed9564a28e7d0765e4da915041ac0313e1a1bcf))


### Bug Fixes

* **deps:** build with Go 1.27.3 ([c8c0a7c](https://github.com/jessechumo/Wera/commit/c8c0a7c4aed6156684e294773c5d5ab5fd5a0d79))
* **deps:** build with Go 1.27.3 ([a3e8db1](https://github.com/jessechumo/Wera/commit/a3e8db1eafb3c7bf273cf9f5422a9a95b0f5bf72))

## [0.5.0](https://github.com/jessechumo/Wera/compare/wera-v0.4.0...wera-v0.5.0) (2026-10-20)


### Features

* **companies:** accept Oracle, Workable and Rippling boards ([1678653](https://github.com/jessechumo/Wera/commit/1678653b106e758a87c86ab9f630aed3712ab8c4))
* **companies:** add 190 employers beyond tech ([2e9ceab](https://github.com/jessechumo/Wera/commit/2e9ceab539f34a56d7508e2a6581f5596a1c49f8))
* **companies:** add 2,688 employers found in public job lists ([faf94ed](https://github.com/jessechumo/Wera/commit/faf94edcf0f00cfcf5948aef697135519a60a353))
* **companies:** add 304 public companies and hospitals ([0b97056](https://github.com/jessechumo/Wera/commit/0b9705677db63e9b3b496e8205294d17c73e194e))
* **companies:** add 384 employers on Oracle, Workable and Rippling ([498743d](https://github.com/jessechumo/Wera/commit/498743d5da09cbdb89cc65f6db35bb409fa3b2b7))
* **companies:** add 987 employers found through Common Crawl ([e695ff2](https://github.com/jessechumo/Wera/commit/e695ff2b76b332b489a7a50ea4418de6657d7a34))
* **companies:** watch the job boards of jobs users add ([96af442](https://github.com/jessechumo/Wera/commit/96af4422a36cbd45503561c67740c24e0d99b7fa))
* **pipeline:** bound the time a run spends on job details ([897a30d](https://github.com/jessechumo/Wera/commit/897a30dcddf4049350d7878e26b13d8c5f6b5865))
* **profile:** write profiles and score jobs for any career ([b8821ef](https://github.com/jessechumo/Wera/commit/b8821ef972d3a537dd48e9d13ad48228e1b972fa))
* **roles:** add healthcare, business, education and trades careers ([c800ea7](https://github.com/jessechumo/Wera/commit/c800ea715eb4e6d88f875496f416e3de76113973))
* **roles:** group role families into career fields ([72103fd](https://github.com/jessechumo/Wera/commit/72103fdcca5065ac6478906b083264fc2115ce69))
* **sources:** read Oracle Recruiting Cloud career sites ([3ce2445](https://github.com/jessechumo/Wera/commit/3ce244525741fd1925156974ce337577b20b08de))
* **sources:** read Rippling job boards ([fb56075](https://github.com/jessechumo/Wera/commit/fb5607555cb9456dcc792a3ceedc72c7d0721fcd))
* **sources:** read the job board from a posting's address ([cdeeecc](https://github.com/jessechumo/Wera/commit/cdeeecc12efd7a8c5d627d0c85442738bcfcef9e))
* **sources:** read Workable job boards ([989d6ae](https://github.com/jessechumo/Wera/commit/989d6aebfb2a527d83bfca44eac80a2f45410bd7))
* support careers beyond tech and 190 more employers ([d891fc4](https://github.com/jessechumo/Wera/commit/d891fc4854b22af452d1444bd4dc72878f280fea))
* thousands of employers, three new job boards, and boards found from added jobs ([660aa6c](https://github.com/jessechumo/Wera/commit/660aa6c78df73a0e2a8a089aafeefab0e774b81c))


### Bug Fixes

* **store:** hand a board to the companies.yaml entry that lists it ([26b858f](https://github.com/jessechumo/Wera/commit/26b858f1cae0ce2c72c688b3c04928638013031b))


### Documentation

* describe career fields and the wider company list ([c19d7df](https://github.com/jessechumo/Wera/commit/c19d7df04ba1917d825302151e36eb422ad0717a))

## [0.4.0](https://github.com/jessechumo/Wera/compare/wera-v0.3.1...wera-v0.4.0) (2026-10-20)


### Features

* **applicant:** ask more application and self-identification questions ([717ddaa](https://github.com/jessechumo/Wera/commit/717ddaa2c13fe5fc085ad35d368b9682f878100a))
* **applicant:** remember answers given on application forms ([cab1713](https://github.com/jessechumo/Wera/commit/cab17131a9f7bf9168de15a5f68054f4859a7fc9))
* **ext:** record each application the extension fills ([a64a89f](https://github.com/jessechumo/Wera/commit/a64a89fb13bb7e00fa9389ed29564c0b530ce254))
* **ext:** suggest answers for a whole form page in one AI call ([4677494](https://github.com/jessechumo/Wera/commit/4677494be488ecf58ae99f9a4ba5b956da58bce9))
* **letters:** steer cover letters and tailored resumes ([40334bb](https://github.com/jessechumo/Wera/commit/40334bbe2483f7c5e921d3132f289eba7b6ac842))
* smarter autofill backend (answers bank, page suggestions, runs, new questions) ([e2ed2d5](https://github.com/jessechumo/Wera/commit/e2ed2d5f7122b286fee8b6c04cb75bad5f871782))
* smarter autofill backend (answers bank, page suggestions, runs, new questions) ([b1c38c8](https://github.com/jessechumo/Wera/commit/b1c38c8cc611dc8fd9778096d918b0949c9b4559))


### Bug Fixes

* **deps:** bump github.com/go-chi/chi/v5 ([41bf088](https://github.com/jessechumo/Wera/commit/41bf0888eaed9ee175f8acb5debdcdff564e12b9))
* **deps:** bump github.com/go-chi/chi/v5 from 5.4.0 to 5.5.0 in the go-modules group ([3db7e45](https://github.com/jessechumo/Wera/commit/3db7e45f050b8228c9779643c060c2b9a472f199))

## [0.3.1](https://github.com/jessechumo/Wera/compare/wera-v0.3.0...wera-v0.3.1) (2026-10-19)


### Documentation

* redraw architecture diagrams with logos and legends ([beaa4ca](https://github.com/jessechumo/Wera/commit/beaa4ca84034ecf7edab53088a78c8fb1e288159))

## [0.3.0](https://github.com/jessechumo/Wera/compare/wera-v0.2.1...wera-v0.3.0) (2026-10-19)


### Features

* **config:** AI_* settings with coral, deepinfra and openai presets ([f4b67ee](https://github.com/jessechumo/Wera/commit/f4b67eeab1a3ce633807f58c8a7d419967c5083e))
* **deploy:** one-folder server deploy with Caddy HTTPS ([b657b7a](https://github.com/jessechumo/Wera/commit/b657b7ac3210d547ef0f8b98621a81e52dfcf69a))
* **deploy:** one-folder server deploy with Caddy HTTPS ([1c4b1c8](https://github.com/jessechumo/Wera/commit/1c4b1c8af228e11152d1b45e43607bf61d29503a))
* **deploy:** run a local embedding model alongside the API ([9002f50](https://github.com/jessechumo/Wera/commit/9002f507a767bcdb93074782abc636337be8be43))
* **embed:** client for OpenAI-compatible embedding endpoints ([b6fc97a](https://github.com/jessechumo/Wera/commit/b6fc97aa66d0eac333176e1053373032716fd766))
* **pipeline:** rank by embeddings and limit AI scores per user ([915f26e](https://github.com/jessechumo/Wera/commit/915f26ec21a430d6918e25f279133bad97f5bf00))
* rank jobs by embeddings and limit per-user AI scores ([f70dded](https://github.com/jessechumo/Wera/commit/f70ddedf6283af540f0590f39adb09ff2d99d1e7))
* **scoring:** provider-neutral client and cost accounting ([0b4a0f7](https://github.com/jessechumo/Wera/commit/0b4a0f74ee99f8fc48321b594df4c276c7f757c8))
* **scoring:** shorter fit replies and a warm prompt cache ([e0668db](https://github.com/jessechumo/Wera/commit/e0668db9e85ba1740070950247d32faddcdf90d7))


### Documentation

* describe embedding ranking and the AI score limits ([f7297e4](https://github.com/jessechumo/Wera/commit/f7297e4aa551db4caf23a94b4bfeec7390917f68))
* document switching AI providers ([dd7d348](https://github.com/jessechumo/Wera/commit/dd7d3484286aaeaf8dbe16990be588d5949f8481))

## [0.2.1](https://github.com/jessechumo/Wera/compare/wera-v0.2.0...wera-v0.2.1) (2026-10-16)


### Bug Fixes

* **deps:** bump chi to 5.4.0 and goose to 3.29.0 ([ae81860](https://github.com/jessechumo/Wera/commit/ae818600081d16066dd0132603f6946a32114f0a))
* **deps:** bump chi to 5.4.0 and goose to 3.29.0 ([f5265c9](https://github.com/jessechumo/Wera/commit/f5265c97af04e25e90e661f200f3735b4e1c5da8))
* **deps:** close 15 security alerts and update CI ([709db14](https://github.com/jessechumo/Wera/commit/709db14b3e6bf46f860810d8057a34ce4fa3e3e4))
* **deps:** update excelize to close 15 security alerts ([32d0c5a](https://github.com/jessechumo/Wera/commit/32d0c5acf1fcc2da1004d64c5819c8c0566ccfd6))

## [0.2.0](https://github.com/jessechumo/Wera/compare/wera-v0.1.0...wera-v0.2.0) (2026-10-14)


### Features

* **admin:** an admin API for the wera-admin app ([be0515e](https://github.com/jessechumo/Wera/commit/be0515e3e7728e04be104a4420c979cb82dd78b5))
* **api:** add a community blog with comments and reactions ([93cb090](https://github.com/jessechumo/Wera/commit/93cb090c551b199299e710b46fbfcd18293b8283))
* **api:** add interview practice questions ([091cd58](https://github.com/jessechumo/Wera/commit/091cd58728512c0c684af1eacd608dc96dcea63d))
* **api:** add profile pictures and resume file viewing ([9193427](https://github.com/jessechumo/Wera/commit/9193427465feff3b78068a9deb5a4a0022f3723c))
* **api:** add resume documents, previews, PDF and tailoring per job ([85d9614](https://github.com/jessechumo/Wera/commit/85d9614cb31ea9457033d66ef04cfefb58b52981))
* **api:** add the browser extension endpoints ([47e5619](https://github.com/jessechumo/Wera/commit/47e56198aec10a77b9a999958ea4f7d85eebf0a2))
* **api:** add user settings, hidden companies and account deletion ([ac863e0](https://github.com/jessechumo/Wera/commit/ac863e0595ba6b97eb079c55d844d0d149c205b0))
* **api:** compare a job with H-1B filings at its wage level ([4c9035a](https://github.com/jessechumo/Wera/commit/4c9035a5472a8f2d6af6f93381b18b0f11831c0f))
* **api:** email verification and password reset, off until configured ([e48b927](https://github.com/jessechumo/Wera/commit/e48b927576a6f2139a92beab7fbe34a433fcb0a6))
* **api:** filter and rank synchronously when a profile is saved ([130a6fc](https://github.com/jessechumo/Wera/commit/130a6fc24628722cef1dcf2ee2f8df6ad01d80aa))
* **api:** find a board job from its company's careers page ([20bb3c4](https://github.com/jessechumo/Wera/commit/20bb3c42f1f13ec3b0b9199d609093a907dd1d7f))
* **api:** generate cover letters for a job ([88ad4f8](https://github.com/jessechumo/Wera/commit/88ad4f89eb4e16e7baeae865053fdc5a44a58266))
* **api:** H-1B filings per company, per employer and per job ([761cc69](https://github.com/jessechumo/Wera/commit/761cc69cac1655b874836fd0bcb57659cd39d556))
* **api:** list industries A to Z ([ff845b0](https://github.com/jessechumo/Wera/commit/ff845b00fa2bdc758396e9edb073855be68c58ce))
* **api:** list role families A to Z ([bade9bd](https://github.com/jessechumo/Wera/commit/bade9bda5046acde27ccce7618ee9b4e8acea9fb))
* **api:** page the sponsorship list and summarize it server-side ([c835976](https://github.com/jessechumo/Wera/commit/c835976698b85468fa3dea09476c31ae3b0f6c0b))
* **api:** public headline numbers for the landing page ([87fd8d7](https://github.com/jessechumo/Wera/commit/87fd8d75e12164e5ac5241abfea37bf14411b1eb))
* **api:** show community authors' profile pictures ([ca0f1b1](https://github.com/jessechumo/Wera/commit/ca0f1b154aebfd0025593890e1d73bc379b06de3))
* **api:** show estimated matches instantly and score jobs on demand ([d4528df](https://github.com/jessechumo/Wera/commit/d4528df264eb8d6d738e092821a999843e1dfe69))
* **api:** summarize visa sponsorship signals by company ([63d439b](https://github.com/jessechumo/Wera/commit/63d439b84ec5ef1d5c71e0f182038f8b124166cb))
* **build:** stamp binaries with their version and commit ([f545f6d](https://github.com/jessechumo/Wera/commit/f545f6ddc7b97e79e63938d6bba71fc8d8c5039b))
* **config:** make the sign-up rate limit configurable ([e673eb6](https://github.com/jessechumo/Wera/commit/e673eb6cfffa2123265cf361175439cfb1159423))
* **config:** name the H-1B filers of 51 companies ([b437747](https://github.com/jessechumo/Wera/commit/b437747143b4e8e9651a823bfb59eae32e1125a1))
* **deploy:** opt-in public API behind a Cloudflare quick tunnel ([28af536](https://github.com/jessechumo/Wera/commit/28af536b6a4f9786ea189c36b9812920b620abd0))
* **deploy:** serve the wera-admin console on port 3001 ([b1d278f](https://github.com/jessechumo/Wera/commit/b1d278fa60b72be285d237412f8370444edf840d))
* **h1b:** load the Department of Labor's H-1B disclosure data ([88d9e1a](https://github.com/jessechumo/Wera/commit/88d9e1ad85b3a9bf71cdde014b170c6d86cbf767))
* **h1b:** load the Department of Labor's H-1B disclosure data ([3fa463c](https://github.com/jessechumo/Wera/commit/3fa463ca1953deeef4504f6abad786adb6ab3195))
* **jobs:** share added jobs with every user they suit ([821e628](https://github.com/jessechumo/Wera/commit/821e62886084e0da8fc47a83a2e0a2ac9ee3723d))
* **mail:** email templates and SMTP delivery ([f0ed098](https://github.com/jessechumo/Wera/commit/f0ed09899d70015376f6b95d0cad8069f1064abd))
* **moderation:** review community content before it is published ([1114972](https://github.com/jessechumo/Wera/commit/1114972e2757dc34d5fd9ed85dfe043f8d182148))
* **notify:** email outbox, verification and reset tokens ([4e11994](https://github.com/jessechumo/Wera/commit/4e11994d9772e2da522e3599748705cbe7402ce7))
* **pipeline:** apply known job facts when filtering ([97e6dec](https://github.com/jessechumo/Wera/commit/97e6decacc1def0496b4d2df2c61948c9563f07d))
* **pipeline:** defer companies during maintenance and catch up after ([dc24aac](https://github.com/jessechumo/Wera/commit/dc24aacbf4702d9187efbdab8b5be445c536fdbe))
* **pipeline:** rank unscored matches locally before LLM scoring ([5011f5f](https://github.com/jessechumo/Wera/commit/5011f5f8448ccb5073bb796179bd19c421ccdf55))
* **profile:** read job pages, draft answers and tailor resumes ([9ee213b](https://github.com/jessechumo/Wera/commit/9ee213b7b672f704d6f3ef58a1f919dc8cc6f182))
* **resume:** four polished templates, bundled fonts, A4 and accents ([c451dc4](https://github.com/jessechumo/Wera/commit/c451dc477394dafb914bfb67c63d113c6e2d831d))
* **resume:** import resumes with AI and tailor them to a job ([25cba19](https://github.com/jessechumo/Wera/commit/25cba19b341989fb2c53b3c39814f8880c8f0b19))
* **resume:** LaTeX in every template, in-site LaTeX editing, a resume review ([4fafbfa](https://github.com/jessechumo/Wera/commit/4fafbfa57249d940d9f5c1eb23384cb9d994a715))
* **resumes:** build the main resume in the background after an upload ([2e93841](https://github.com/jessechumo/Wera/commit/2e938414f86088a42f6cd839080cc7d09bc4e518))
* **resume:** structured resumes rendered in the classic LaTeX layout ([f9dfaf0](https://github.com/jessechumo/Wera/commit/f9dfaf0997a292af30b228ade1805e8174d731ae))
* **resume:** suggest stronger rewrites of a bullet ([8a02b62](https://github.com/jessechumo/Wera/commit/8a02b622ea81817b75b48a2bf7f9ae829a524224))
* **store:** add extension tokens, application details and added jobs ([575a9fe](https://github.com/jessechumo/Wera/commit/575a9fe71eeac3b289f9ba09b79a9f8594fa74a5))
* **store:** add extension tokens, application details and added jobs ([3a8b660](https://github.com/jessechumo/Wera/commit/3a8b6600913bc42ee1b8ef6c4bf948f36c5ef039))
* **workday:** recognize job board maintenance ([795057e](https://github.com/jessechumo/Wera/commit/795057ec1946eec560651d0ee7cd39936826d3d1))
* **workday:** recognize job board maintenance ([44236a3](https://github.com/jessechumo/Wera/commit/44236a30fa4b5ba25e588f018ec88f7694d47bd0))


### Bug Fixes

* **api:** a status-only application update keeps the saved notes ([84ec9fb](https://github.com/jessechumo/Wera/commit/84ec9fb2eaf947040d87e8eae08b5dce8367dcbe))
* **api:** cap application bodies and add server timeouts ([c167658](https://github.com/jessechumo/Wera/commit/c1676585305dd57a349d8124ddac64998b53137f))
* **api:** check the AI is configured before counting a request ([1998c05](https://github.com/jessechumo/Wera/commit/1998c055136c96110a206744b5387508f570dc0e))
* **api:** count new matches per day in the user's time zone ([0182b99](https://github.com/jessechumo/Wera/commit/0182b993540fd64abf8b36388fbdd93ca3f6e75e))
* **api:** count new matches per day in the user's time zone ([6580b52](https://github.com/jessechumo/Wera/commit/6580b52f4c9bc1933bac6d8906a742b1bea71cfd))
* **api:** find a saved job from its /apply page ([7810ed4](https://github.com/jessechumo/Wera/commit/7810ed49554fd52c3abe3e5a44701af2b416b15b))
* **api:** read jobs saved without a location ([3f2c535](https://github.com/jessechumo/Wera/commit/3f2c5359e2d41b6d0c10be91d2b35877483a9f29))
* **api:** read jobs saved without a location ([52638c2](https://github.com/jessechumo/Wera/commit/52638c2fe4ddd7d1187e6dd762eddf7cd6802e24))
* **auth:** limit logins per account, stop IP spoofing, add headers ([b2d2f0a](https://github.com/jessechumo/Wera/commit/b2d2f0aa0946d247df6ff417f8fd5d6c056da412))
* **config:** require HTTPS for the inference API ([7639ed7](https://github.com/jessechumo/Wera/commit/7639ed7458d2249a0510f27051598bf8e56bb5ca))
* **deps:** upgrade excelize, x/net and x/crypto for known vulnerabilities ([c0be4b6](https://github.com/jessechumo/Wera/commit/c0be4b67acd383dff6d05e8c6b6ce895ae71523a))
* **deps:** upgrade pgx, x/image and x/text for security advisories ([4004a2e](https://github.com/jessechumo/Wera/commit/4004a2e2ede502695278100f4e8c91424defc26b))
* **jobs:** keep shared jobs when the account that added them is deleted ([6997245](https://github.com/jessechumo/Wera/commit/6997245100c2d439805ab0af3e368a7b5fd9e7ed))
* **jobs:** keep shared jobs when the account that added them is deleted ([6e71046](https://github.com/jessechumo/Wera/commit/6e71046a5abcc99be56d8f4945cb168941d8fbdb))
* **llm:** fence untrusted text in every prompt ([5e831ed](https://github.com/jessechumo/Wera/commit/5e831ed0c15d94efa799ec5eed1a2d02bdbd0b5b))
* **notify:** keep digest reasons to one short sentence ([89a0fb7](https://github.com/jessechumo/Wera/commit/89a0fb7a98ed527e617c9190876895e0cea8202e))
* **pipeline:** restore existing scores when jobs are refiltered ([b7cdf95](https://github.com/jessechumo/Wera/commit/b7cdf954986c84913f558913407c2f316d4b4ea8))
* **profile:** send empty preference lists instead of null ([f2c2e38](https://github.com/jessechumo/Wera/commit/f2c2e38df22c6cac2a0e7072b24c417840d397f6))
* **relevance:** scale estimates by similarity so top matches differ ([4318126](https://github.com/jessechumo/Wera/commit/4318126c0e113b33bb8efdda262c068d1bcd857c))
* **resume:** count a keyword met when any named alternative appears ([37ac697](https://github.com/jessechumo/Wera/commit/37ac697407f3934e7d655e84efa1d51222890c1b))
* **resume:** hide bullets in a tailored copy only when the page is full ([ae4f280](https://github.com/jessechumo/Wera/commit/ae4f28037e32fab68481b8550b47ae738ddd6bf4))
* **resume:** readable dashes in LaTeX, no short-bullet nag on education ([ed485df](https://github.com/jessechumo/Wera/commit/ed485df5b70fa4fd76fa3cb2efbbac43efd4c69d))
* **sources:** retry when a response breaks off mid-body ([a3dceca](https://github.com/jessechumo/Wera/commit/a3dcecaf1659f53c3550c62a3c97345c21f26f1e))
* **sources:** stop retrying when a board asks to wait over a minute ([5c17876](https://github.com/jessechumo/Wera/commit/5c178764115145ca47abda99245ee9c29dabda13))


### Performance

* **api:** cache H-1B and sponsorship answers ([7b67bcf](https://github.com/jessechumo/Wera/commit/7b67bcf66582e806dba9cd6676ed5840ee1d6eca))
* **api:** cache H-1B and sponsorship answers ([a3eb94a](https://github.com/jessechumo/Wera/commit/a3eb94a2ac6592029203d278a8c6549b70440d2a))
* **api:** share one computation between concurrent cache misses ([b035fa4](https://github.com/jessechumo/Wera/commit/b035fa423e9ee9e5b67ee8fa61c83340cffa3ac9))
* **fetch:** send ETags so unchanged boards answer 304 ([38b0d33](https://github.com/jessechumo/Wera/commit/38b0d33d172f4be87c86a08cc0f8f4b401d0c942))
* **filter:** filter a new user's 40k jobs in seconds ([303e34c](https://github.com/jessechumo/Wera/commit/303e34c0d0f3c357d915b1b3fa2e9c8bffbd1098))
* **pipeline:** prune matches for long-closed postings ([4d1a66b](https://github.com/jessechumo/Wera/commit/4d1a66be193f43398c576b998efce7424a226e58))
* **scoring:** extract job facts once and share them across users ([e90fdf2](https://github.com/jessechumo/Wera/commit/e90fdf2be09c8f69d4a0c127e7108c60fe2ab723))
* **scoring:** score in priority order and save each result as it lands ([63f3c04](https://github.com/jessechumo/Wera/commit/63f3c04af9eb7f96c8f76f5adc91cf52a1982a97))
* **store:** index the extension's job lookups ([90d8498](https://github.com/jessechumo/Wera/commit/90d8498db3759bd83ff31aea9a43fa347b6b29e9))
* **store:** write only new or changed postings ([c7e01fa](https://github.com/jessechumo/Wera/commit/c7e01fa439dd44db699222fd2b3fed49f734409d))
* **workday:** list only new postings, sweep the full board daily ([11c8bfd](https://github.com/jessechumo/Wera/commit/11c8bfd92585a643a60bbee988890fc0b873aff4))


### Refactoring

* **api:** parse URL ids in one place ([9338ac1](https://github.com/jessechumo/Wera/commit/9338ac1697d305395b5010e5548b916d8e421641))
* wrap errors with %w and compare them with errors.Is ([6c8f641](https://github.com/jessechumo/Wera/commit/6c8f64136d3045497c21d1c1e4c379cf84ed41cc))


### Documentation

* add a security policy and summary of protections ([886f8d1](https://github.com/jessechumo/Wera/commit/886f8d173ea47bd86adc4c395e61f8a19317f7fb))
* add architecture and pipeline diagrams, refresh the README ([a7c9cf1](https://github.com/jessechumo/Wera/commit/a7c9cf1ed97ec765a9bb50d287dd288b54b4a383))
* describe email, its settings and the DNS records it needs ([b82db95](https://github.com/jessechumo/Wera/commit/b82db95ae64bf2f1867c8afb3e999376b5af11ef))
* describe resumes, tailoring and the typst requirement ([e5e1d88](https://github.com/jessechumo/Wera/commit/e5e1d88780ac1322d00a1f671442f741024693b8))
* describe the Chrome extension API in the README ([b8db0ba](https://github.com/jessechumo/Wera/commit/b8db0ba9b697f1d48a4c3bfb0ad03421baf43d2a))
* describe the H-1B data and how to load it ([79642bf](https://github.com/jessechumo/Wera/commit/79642bf9535e175b9303aa8585a7f3192f50ad5d))
* describe the resume templates, LaTeX export and review ([cf688d0](https://github.com/jessechumo/Wera/commit/cf688d0ab190ca360d066b4ae8a7aaaf90dacff3))
* README — add wera deep to quick start ([fb5e651](https://github.com/jessechumo/Wera/commit/fb5e65134e50000f8bf723a24d4564ca9ce83cb1))
