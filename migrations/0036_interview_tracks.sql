-- +goose Up
-- Interview practice by role and stack: questions get stack tags (go,
-- react, kubernetes, ...) that a round can prefer, a "product" kind, and
-- new domains for the role tracks (internal/interview/tracks.go). Work
-- simulations ship in the binary (internal/interview/scenarios.json); only
-- the replies a user picks are stored here.
ALTER TABLE interview_questions ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE interview_questions DROP CONSTRAINT interview_questions_kind_check;
ALTER TABLE interview_questions ADD CONSTRAINT interview_questions_kind_check CHECK (kind IN ('technical','behavioral','product'));

UPDATE interview_questions SET tags = '{sql,postgres}' WHERE domain = 'Databases';
UPDATE interview_questions SET tags = '{linux,networking}' WHERE domain = 'Operating systems & networking';
UPDATE interview_questions SET tags = '{python}' WHERE domain = 'Machine learning';

CREATE TABLE interview_scenario_answers (
  id          BIGSERIAL PRIMARY KEY,
  user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scenario    TEXT NOT NULL,
  step        SMALLINT NOT NULL CHECK (step >= 0),
  choice      SMALLINT NOT NULL CHECK (choice BETWEEN 0 AND 3),
  points      SMALLINT NOT NULL CHECK (points BETWEEN 0 AND 2),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX interview_scenario_answers_user_idx ON interview_scenario_answers (user_id, scenario, created_at DESC);

INSERT INTO interview_questions (domain, kind, difficulty, tags, question, choices, answer, explanation) VALUES
-- Web & frontend
('Web & frontend','technical','easy','{javascript}','What does === do that == does not in JavaScript?',
 ARRAY['Compares objects by their contents','Compares without converting types first','Runs faster but is otherwise the same','Works only on numbers'],1,'== converts types before comparing (so 0 == "" is true); === compares type and value as they are. Objects are compared by reference with either.'),
('Web & frontend','technical','easy','{css}','Which CSS layout is built for laying items out along one row or one column?',
 ARRAY['Grid','Float','Flexbox','Table layout'],2,'Flexbox works along one axis at a time; Grid handles rows and columns together.'),
('Web & frontend','technical','easy','{html,accessibility}','A clickable div opens a menu. What is the simplest fix that makes it work for keyboard and screen reader users?',
 ARRAY['Use a <button> element','Add a title attribute','Add cursor: pointer','Add an onMouseOver handler'],0,'A real <button> is focusable, fires on Enter and Space, and is announced as a button, with no extra ARIA or key handlers.'),
('Web & frontend','technical','medium','{javascript}','What does this log? for (var i = 0; i < 3; i++) setTimeout(() => console.log(i))',
 ARRAY['0 1 2','undefined three times','An error','3 3 3'],3,'var is function-scoped, so all three callbacks share one i, which is 3 by the time they run. With let, each iteration gets its own binding and it logs 0 1 2.'),
('Web & frontend','technical','medium','{react}','A child component re-renders every time its parent does, though its props never change. What helps most?',
 ARRAY['Give it a key that changes on every render','Wrap it in React.memo and keep its props stable (useMemo/useCallback)','Turn it into a class component','Move all its state into a global store'],1,'React.memo skips rendering when props are shallowly equal, but only if the parent does not create new objects or functions for them on every render.'),
('Web & frontend','technical','medium','{react}','Why does React warn when list items have no key?',
 ARRAY['Without stable keys React cannot match items between renders, so it may reuse the wrong DOM and state','Keys are needed for CSS selectors','Keys make lists sortable','Screen readers need keys'],0,'Keys tell React which item is which across renders. Index keys break when items are inserted, removed or reordered.'),
('Web & frontend','technical','medium','{browser,performance}','Which change most improves Largest Contentful Paint on a landing page with a big hero image?',
 ARRAY['Lazy-load the hero image','Move the CSS to the end of the body','Add a loading animation','Preload the hero, size it properly and serve a modern format such as AVIF or WebP'],3,'The hero is usually the LCP element. Lazy-loading it delays it; preloading, right-sizing and a smaller format make it paint sooner.'),
('Web & frontend','technical','hard','{javascript}','In what order does this log? console.log(1); setTimeout(() => console.log(2)); Promise.resolve().then(() => console.log(3)); console.log(4)',
 ARRAY['1 2 3 4','1 3 4 2','1 4 3 2','1 4 2 3'],2,'Synchronous code runs first (1, 4). Then the microtask queue (promise callbacks: 3) empties before the next task (the timer: 2).'),
('Web & frontend','technical','hard','{typescript}','What does TypeScript''s unknown make you do that any does not?',
 ARRAY['Narrow the value with a check before you use it','Declare it with const','Convert it to a string','Nothing, they are the same'],0,'any turns type checking off; unknown accepts any value but will not let you use it until a typeof, instanceof or other check narrows it.'),
('Web & frontend','technical','medium','{browser,security}','Your app at app.example.com calls api.example.com and the browser blocks the response. What fixes it?',
 ARRAY['Disable CORS in the browser','The API returns Access-Control-Allow-Origin for app.example.com (and handles the preflight)','Use HTTP instead of HTTPS','Put the API key in the URL'],1,'Browsers enforce the same-origin policy; the server opts in with CORS headers. It is a server setting, not a client one.'),
-- APIs & backend
('APIs & backend','technical','easy','{http}','A signed-in user asks for a resource they are not allowed to see. Which status code fits?',
 ARRAY['401 Unauthorized','400 Bad Request','403 Forbidden','500 Internal Server Error'],2,'401 means not authenticated; 403 means authenticated but not allowed. Some APIs return 404 instead to hide that the resource exists.'),
('APIs & backend','technical','easy','{http}','Which HTTP method is idempotent by definition?',
 ARRAY['PUT','POST','PATCH','None of them'],0,'GET, PUT and DELETE are idempotent: repeating them leaves the same state. POST is not, and PATCH is not guaranteed to be.'),
('APIs & backend','technical','medium','{sql,http}','A list endpoint pages through millions of rows while new rows keep arriving. Which pagination holds up best?',
 ARRAY['OFFSET and LIMIT','Return everything and let the client page','Cursor (keyset) pagination on an indexed, unique sort key','Random sampling'],2,'Keyset pagination ("after id 123") stays fast at any depth and does not skip or repeat rows when inserts shift the offsets.'),
('APIs & backend','technical','medium','{go}','In Go, a goroutine sends on an unbuffered channel that nobody reads. What happens?',
 ARRAY['The value is dropped','The runtime buffers it','It panics','The goroutine blocks until a receiver is ready'],3,'An unbuffered send waits for a receiver. If none ever comes the goroutine leaks, and if every goroutine is blocked the runtime reports a deadlock.'),
('APIs & backend','technical','medium','{go}','What is a Go context.Context mainly for in a request handler?',
 ARRAY['Carrying cancellation and deadlines (and request-scoped values) down to every call','Storing global configuration','Replacing function arguments','Logging'],0,'When the client disconnects or the deadline passes, the context is cancelled, so database queries and outgoing calls stop instead of doing wasted work.'),
('APIs & backend','technical','medium','{python}','Why do CPU-bound threads rarely speed up a task in standard CPython?',
 ARRAY['Python has no threads','The OS limits Python to one core','The Global Interpreter Lock lets only one thread run Python bytecode at a time','Threads use too much memory'],2,'Use multiprocessing (or native extensions) for CPU work; threads still help with I/O. Free-threaded builds without the GIL exist since 3.13 but are not yet the default.'),
('APIs & backend','technical','medium','{queues}','A worker takes a job from a queue, does it, then crashes before acknowledging. With at-least-once delivery, what must the handler be?',
 ARRAY['Fast','Idempotent','Stateless','Synchronous'],1,'The job will be delivered again, so running it twice must have the same effect as once, for example by recording a job id or using upserts.'),
('APIs & backend','technical','hard','{sql}','Two requests each read a balance of 100, subtract 30 and write back. The balance ends at 70. What fixes it most directly?',
 ARRAY['A cache in front of the database','Retrying on failure','A bigger database','An atomic update (SET balance = balance - 30) or a row lock (SELECT ... FOR UPDATE)'],3,'This is a lost update. Do the change in one atomic statement, or lock the row so the second request waits for the first.'),
('APIs & backend','technical','hard','{reliability}','Your service calls a flaky dependency. Which combination prevents a retry storm?',
 ARRAY['Timeouts, capped retries with exponential backoff and jitter, and a circuit breaker','Unlimited immediate retries','Longer timeouts alone','Retrying on every status code'],0,'Backoff with jitter spreads retries out, the cap bounds them, and a circuit breaker stops calling a dependency that is down so it can recover.'),
('APIs & backend','technical','easy','{java}','In Java, why should a class that overrides equals also override hashCode?',
 ARRAY['The compiler requires it','So that equal objects land in the same bucket of a HashMap or HashSet','To make it faster','For serialization'],1,'Hash-based collections look in the bucket that hashCode picks before calling equals. Equal objects with different hash codes get "lost".'),
-- Cloud & DevOps
('Cloud & DevOps','technical','easy','{docker}','What is the difference between a Docker image and a container?',
 ARRAY['They are the same thing','A container is an image saved to disk','An image is a read-only template; a container is a running (or stopped) instance of it','Images run only on Linux'],2,'You build an image once and start many containers from it, each with its own writable layer.'),
('Cloud & DevOps','technical','easy','{docker}','Why copy the dependency files and install dependencies before copying the rest of the source in a Dockerfile?',
 ARRAY['Layer caching: a code change no longer reruns the slow install','It makes the image smaller','Docker requires that order','It makes the app run faster'],0,'Each instruction is a cached layer. If the lockfile has not changed, the install layer is reused and builds take seconds.'),
('Cloud & DevOps','technical','medium','{kubernetes}','A pod is in CrashLoopBackOff. What do you look at first?',
 ARRAY['Delete the deployment and recreate it','Scale it up','Upgrade the cluster','The previous container''s logs (kubectl logs --previous) and the events in kubectl describe pod'],3,'The logs of the crashed container usually say why it exits; the events show image pull errors, OOM kills and failing probes.'),
('Cloud & DevOps','technical','medium','{kubernetes}','What happens when a readiness probe fails?',
 ARRAY['The container restarts','The pod is taken out of its Service''s endpoints until the probe passes again','The pod is deleted','Nothing'],1,'Readiness controls traffic; liveness controls restarts. Mixing them up causes restart loops under load.'),
('Cloud & DevOps','technical','medium','{sre}','A service has a 99.9% monthly availability target. Roughly how much downtime does that allow in a 30-day month?',
 ARRAY['About 4 minutes','About 7 hours','About 43 minutes','About 3.6 days'],2,'0.1% of 43,200 minutes is 43.2 minutes. That is the error budget: spend it on releases and experiments, and slow down when it runs out.'),
('Cloud & DevOps','technical','medium','{linux}','df says a disk is 100% full, but du finds far less data. What is a likely cause?',
 ARRAY['A deleted file that a running process still holds open','Bad sectors','Too many directories','The disk is mounted read-only'],0,'The space is freed only when the last open handle closes. lsof +L1 finds such files; restarting the process (or truncating the file) frees the space.'),
('Cloud & DevOps','technical','medium','{deploys}','Which release strategy sends a small share of traffic, say 5%, to the new version first, so you can roll back quickly?',
 ARRAY['Recreate','Big bang','Rolling restart','Canary'],3,'A canary exposes a slice of real traffic to the new version and compares its error rate and latency before the rest moves over.'),
('Cloud & DevOps','technical','hard','{terraform}','Two engineers run terraform apply against the same state at the same time. What prevents a corrupted state?',
 ARRAY['A remote backend with state locking','Running plan first','Workspaces','Nothing can'],0,'Remote backends such as S3 (with its lock file), Terraform Cloud or GCS take a lock for each run, so the second apply waits or fails.'),
('Cloud & DevOps','technical','easy','{observability}','Which signal is best for seeing where the time goes across services in a single request?',
 ARRAY['CPU metrics','Distributed traces','Grepping the logs','Uptime checks'],1,'A trace follows one request through every service as a tree of timed spans, so the slow hop stands out.'),
('Cloud & DevOps','technical','hard','{aws,networking}','Instances in a private subnet can reach the internet but nothing on the internet can start a connection to them. What gives them outbound access?',
 ARRAY['An internet gateway attached to the private subnet','A public IP on each instance','A NAT gateway in a public subnet, routed from the private subnet','A security group rule allowing all traffic'],2,'The NAT gateway sends outbound traffic out and lets replies back in, but accepts no new inbound connections. Note it charges per GB processed.'),
-- Mobile
('Mobile','technical','easy','{ios,android}','Why must network calls stay off the main (UI) thread?',
 ARRAY['The OS forbids networking entirely there','They use more battery there','A blocked main thread freezes the UI and can get the app killed (ANR on Android, watchdog on iOS)','Only for security'],2,'The main thread draws the UI and handles touches. Do the work in the background and post results back to the main thread.'),
('Mobile','technical','medium','{android,kotlin}','An Android screen loses what the user typed when they rotate the phone. Why?',
 ARRAY['A bug in the emulator','Rotation is a configuration change that recreates the Activity; keep state in a ViewModel or saved state','The device is low on memory','The layout file is wrong'],1,'By default the Activity is destroyed and recreated on rotation. A ViewModel survives configuration changes; saved state also survives the process being killed.'),
('Mobile','technical','medium','{ios,swift}','In Swift, what does [weak self] in a closure prevent?',
 ARRAY['Data races','Optional unwrapping','Slow execution','A retain cycle between an object and a closure it holds'],3,'If self holds the closure and the closure strongly captures self, neither is ever freed. A weak capture breaks the cycle.'),
('Mobile','technical','medium','{offline}','What is the usual way to make a feed work offline?',
 ARRAY['Store data locally (a database), show it first, and sync with the server when online, resolving conflicts','Show an error until the network returns','Retry the request in a loop','Bundle all data into the app'],0,'Offline-first apps read from local storage and treat the network as a sync, which also makes them feel faster online.'),
('Mobile','technical','hard','{ios,android}','A crash happens only in release builds of the new version. What helps most?',
 ARRAY['Console logs','The simulator','Asking users to email you','Crash reports with symbolicated or deobfuscated stack traces (dSYMs, R8 mapping files)'],3,'Release builds are optimized and obfuscated. Without the symbol or mapping files, stack traces are unreadable addresses.'),
('Mobile','technical','easy','{releases}','Why roll a mobile release out in stages, say 1%, then 10%, then 100%?',
 ARRAY['App stores require it','You cannot pull back a binary users have installed, so stages limit how many get a bad build','It makes the app smaller','It speeds up review'],1,'A web deploy can be rolled back in minutes; an installed app cannot. Staged rollouts and remote flags are the mobile safety net.'),
-- Security
('Security','technical','easy','{auth}','How should a server store user passwords?',
 ARRAY['With a slow, salted password hash such as Argon2, scrypt or bcrypt','Encrypted with AES so support can recover them','As plain SHA-256 hashes','In plain text in a locked-down table'],0,'Slow salted hashes make each guess expensive and stop precomputed tables. Fast hashes such as SHA-256 can be guessed billions of times a second.'),
('Security','technical','easy','{sql}','What prevents SQL injection most reliably?',
 ARRAY['Escaping quotes by hand','Hiding database errors','Parameterized queries (prepared statements)','Using a NoSQL database'],2,'Parameters are sent separately from the SQL text, so user input is never parsed as code.'),
('Security','technical','medium','{web}','What does a cross-site request forgery (CSRF) attack rely on?',
 ARRAY['A stolen password','A weak TLS cipher','An unvalidated file upload','The browser sending the victim''s cookies along with a request another site made'],3,'Defenses: SameSite cookies, anti-CSRF tokens, and checking the Origin header on requests that change state.'),
('Security','technical','medium','{web,javascript}','A page shows a user''s display name with innerHTML. What is the risk?',
 ARRAY['Slow rendering','Cross-site scripting: a name containing markup or script runs in other users'' browsers','SQL injection','None, if names are short'],1,'Insert text with textContent (or let a framework escape it), and add a Content Security Policy as a second line of defense.'),
('Security','technical','hard','{auth}','A server verifies JWTs using whatever "alg" the token header names. What is the classic attack?',
 ARRAY['A token with alg "none" (or a switched algorithm) that verifies without the real key','A replay of an old token','Brute-forcing the payload','Clock skew'],0,'Servers must pin the algorithms they accept. Otherwise an attacker picks one that needs no secret, or uses the public key as an HMAC secret.'),
('Security','technical','medium','{least-privilege}','What does least privilege mean for a service''s database user?',
 ARRAY['Use the admin user to avoid permission errors','Share one user across services','Rotate the password every day','Grant only what the service needs, with no DROP and no access to other schemas'],3,'If the service is compromised, the damage is limited to what its user can do.'),
-- Data & analytics
('Data & analytics','technical','easy','{sql}','Which SQL clause filters groups after aggregation?',
 ARRAY['WHERE','GROUP BY','HAVING','ORDER BY'],2,'WHERE filters rows before grouping; HAVING filters the groups, for example HAVING count(*) > 5.'),
('Data & analytics','technical','medium','{sql}','users LEFT JOIN orders ... WHERE orders.status = ''paid''. What happens to users with no orders?',
 ARRAY['They stay, with NULLs','They are dropped: the WHERE on the right table removes the NULL rows, so it acts like an inner join','The query fails','They appear twice'],1,'Move the condition into the join (ON ... AND orders.status = ''paid'') to keep users without paid orders.'),
('Data & analytics','technical','hard','{sql}','What does ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC) = 1 pick out?',
 ARRAY['The first row of the table','The user with the most rows','A random row per user','Each user''s most recent row'],3,'The window numbers each user''s rows from newest to oldest, so row 1 is the latest. Wrap it in a subquery or use QUALIFY to filter.'),
('Data & analytics','technical','easy','{statistics}','Income data is heavily skewed by a few very high earners. Which number best describes a typical person?',
 ARRAY['The median','The mean','The maximum','The standard deviation'],0,'A few extreme values pull the mean up; the median is the middle person and does not move.'),
('Data & analytics','technical','medium','{experimentation}','An A/B test reached p = 0.04 after the team checked it every day and stopped as soon as it was significant. What is wrong?',
 ARRAY['Nothing, 0.04 is below 0.05','The sample is too large','Checking repeatedly and stopping early inflates the false positive rate well above 5%','p-values only work for revenue'],2,'Fix the sample size in advance, or use a method built for continuous monitoring (sequential tests).'),
('Data & analytics','technical','medium','{analysis}','Average session length jumped 30% overnight with no release. What do you check first?',
 ARRAY['Data quality: tracking changes, bot traffic, logging or timezone bugs, and whether one segment explains it','Celebrate it in the weekly update','Launch an A/B test','Change how the dashboard computes it'],0,'Most sudden metric jumps are measurement problems. Rule those out, then break the change down by segment.'),
('Data & analytics','technical','hard','{statistics}','A treatment wins within every age group but loses when all groups are combined. What explains it?',
 ARRAY['A bug in the data','Random noise','Too few age groups','Simpson''s paradox: the groups differ in size and baseline, so pooling reverses the effect'],3,'When group mix differs between arms, compare within groups (or weight them) rather than pooling.'),
-- Product sense
('Product sense','product','easy','{metrics}','What makes a good north-star metric?',
 ARRAY['It counts features shipped','It captures the value customers get and predicts long-term results','It counts page views','It is revenue, always'],1,'Something like "weekly teams that complete a project" tracks real value, and revenue follows it, unlike vanity counts.'),
('Product sense','product','easy','{specs}','What should a product requirements doc lead with?',
 ARRAY['The mockups','The launch date','The problem, who has it, and how success will be measured','The tech stack'],2,'If the problem and the success measure are clear, design and engineering can find better solutions than the first idea.'),
('Product sense','product','medium','{prioritization}','Three teams each say their request is the top priority. What helps compare them?',
 ARRAY['Score each on reach, impact, confidence and effort (RICE) against the current goal','Whoever asks most often','First come, first served','Let each team build its own'],0,'A shared yardstick turns a debate about who matters into one about evidence. The scores start the conversation; they do not end it.'),
('Product sense','product','medium','{discovery}','Few users adopt a new feature. What is the best first step?',
 ARRAY['Add more features to it','Send a push notification to everyone','Remove it','Look at the funnel to see where people drop off, and talk to a few of them'],3,'Data shows where people drop off; conversations show why.'),
('Product sense','product','hard','{metrics,experimentation}','A test raises clicks 8% but support tickets rise 15%. How do you decide?',
 ARRAY['Ship it, since clicks were the goal metric','Revert it without looking further','Treat tickets as a guardrail: find out why they rose and whether the gain survives the cost before shipping','Run it longer and decide then'],2,'Guardrail metrics catch wins that hurt something else. Understand the trade-off before shipping.'),
('Product sense','product','medium','{estimation}','How would you estimate the market for a dog-walking app in one city?',
 ARRAY['Look it up and quote the number','Top down: households, share with dogs, share that pay for walks, times walks a week and the price','Count the competitors','Guess, then adjust'],1,'Interviewers want structured assumptions you can state and change, not the exact answer.'),
-- Behavioral
('Behavioral','behavioral','easy','{}','An interviewer asks "Why do you want to work here?" Which answer lands best?',
 ARRAY['The pay is great','You have applied to many places','Specific reasons tied to their product, their team and your own experience','You need a job soon'],2,'Show you have done your homework and connect what they do to what you have done and want to do.'),
('Behavioral','behavioral','easy','{}','At the end, the interviewer asks "Do you have any questions for us?" What is best?',
 ARRAY['Questions about the team''s work, its current challenges and how success is measured in the role','No, you covered everything','Start with the vacation policy','Ask how you did'],0,'Good questions show interest and help you decide too. Save benefits questions for later in the process.'),
('Behavioral','behavioral','medium','{}','Midway through a technical answer, the interviewer looks lost. What should you do?',
 ARRAY['Keep going; it will make sense at the end','Speak faster to finish sooner','Start over from the beginning','Pause, check where they are, and adjust the level of detail'],3,'Interviews are a conversation. Checking in shows you can communicate, which is part of what is being assessed.'),
('Behavioral','behavioral','medium','{}','A recruiter asks for your salary expectations early on. What works best?',
 ARRAY['Name a number far below the market to seem flexible','Give a researched range for the role and location, or ask for the budgeted range first','Refuse to answer','Tell them your current salary'],1,'Research the role on salary sites and in posted pay ranges. A range anchored to the market keeps you in the process without underselling yourself.');

-- +goose Down
DELETE FROM interview_questions WHERE domain IN ('Web & frontend','APIs & backend','Cloud & DevOps','Mobile','Security','Data & analytics','Product sense');
DELETE FROM interview_questions WHERE domain = 'Behavioral' AND cardinality(tags) = 0 AND question IN (
  'An interviewer asks "Why do you want to work here?" Which answer lands best?',
  'At the end, the interviewer asks "Do you have any questions for us?" What is best?',
  'Midway through a technical answer, the interviewer looks lost. What should you do?',
  'A recruiter asks for your salary expectations early on. What works best?');
DROP TABLE interview_scenario_answers;
ALTER TABLE interview_questions DROP CONSTRAINT interview_questions_kind_check;
ALTER TABLE interview_questions ADD CONSTRAINT interview_questions_kind_check CHECK (kind IN ('technical','behavioral'));
ALTER TABLE interview_questions DROP COLUMN tags;
