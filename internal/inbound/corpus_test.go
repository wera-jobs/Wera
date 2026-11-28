package inbound

// The 50-email test of #201: replies in the shapes the common applicant
// tracking systems and recruiters send them (Greenhouse, Lever, Ashby,
// Workday, iCIMS, SmartRecruiters, a recruiter's own mailbox, scheduling
// tools), forwarded by a filter or by hand, plus mail that is not about a
// tracked job at all. Written before the rules, all invented: no real
// person's mail.

// corpusApps is the tracker the corpus is matched against.
var corpusApps = []App{
	{JobID: 1, Company: "Stripe", Token: "stripe", Title: "Backend Engineer", Status: "applied"},
	{JobID: 2, Company: "Stripe", Token: "stripe", Title: "Data Engineer", Status: "applied"},
	{JobID: 3, Company: "Datadog", Token: "datadog", Title: "Site Reliability Engineer", Status: "applied"},
	{JobID: 4, Company: "Figma", Token: "figma", Title: "Product Designer", Status: "interviewing"},
	{JobID: 5, Company: "Notion", Token: "notion", Title: "Software Engineer, New Grad", Status: "applied"},
	{JobID: 6, Company: "Airtable", Token: "airtable", Title: "Solutions Engineer", Status: "applied"},
	{JobID: 7, Company: "Ramp", Token: "ramp", Title: "Financial Analyst", Status: "saved"},
	{JobID: 8, Company: "Plaid", Token: "plaid", Title: "Software Engineer, Infrastructure", Status: "interviewing"},
	{JobID: 9, Company: "Duolingo", Token: "duolingo", Title: "Data Scientist", Status: "applied"},
	{JobID: 10, Company: "Robinhood", Token: "robinhood", Title: "Security Engineer", Status: "applied"},
	{JobID: 11, Company: "Brex", Token: "brex", Title: "Staff Accountant", Status: "applied"},
	{JobID: 12, Company: "Rippling", Token: "rippling", Title: "Implementation Manager", Status: "interviewing"},
	{JobID: 13, Company: "Anduril Industries", Token: "andurilindustries", Title: "Mechanical Engineer", Status: "applied"},
	{JobID: 14, Company: "Scale AI", Token: "scaleai", Title: "Operations Associate", Status: "applied"},
	{JobID: 15, Company: "Gusto", Token: "gusto", Title: "Customer Success Manager", Status: "applied"},
}

type corpusEmail struct {
	from, subject, body string
	job                 int64  // the tracked job it is about; 0 for none
	kind                string // what it says; "" when it says nothing for the tracker
}

var corpus = []corpusEmail{
	// Rejections.
	{"Stripe Recruiting <no-reply@us.greenhouse-mail.io>", "Your application to Stripe",
		"Hi Alex,\n\nThank you for your interest in the Backend Engineer role at Stripe. After careful consideration, we have decided to move forward with other candidates whose experience more closely matches our needs at this time.\n\nWe wish you the best in your search.\n\nStripe Recruiting", 1, "rejection"},
	{"Datadog <no-reply@hire.lever.co>", "Datadog | Site Reliability Engineer",
		"Hi Alex,\n\nThanks for applying to Datadog for the Site Reliability Engineer position. Unfortunately, we will not be moving forward with your application at this time. We'll keep your resume on file for future openings.\n\nBest,\nThe Datadog Talent Team", 3, "rejection"},
	{"Duolingo Careers <careers@duolingo.com>", "Update on your Duolingo application",
		"Hello Alex,\n\nThank you for taking the time to apply for the Data Scientist role. We received many strong applications and, unfortunately, we have decided not to proceed with your candidacy.\n\nDuolingo Recruiting", 9, "rejection"},
	{"Robinhood <notifications@ashbyhq.com>", "Robinhood - Security Engineer",
		"Hi Alex, we appreciate your interest in Robinhood. Although your background is impressive, we've decided to pursue other candidates for the Security Engineer role. Thanks again for applying.", 10, "rejection"},
	{"Brex Talent <talent@brex.com>", "Your application for Staff Accountant",
		"Hi Alex,\n\nThank you for applying to Brex. We regret to inform you that the position has been filled. We encourage you to apply for future roles that match your skills.\n\nWarm regards,\nBrex Talent", 11, "rejection"},
	{"Gusto Recruiting <gusto@myworkday.com>", "Gusto: Customer Success Manager - Application Status",
		"Dear Alex,\n\nThank you for your interest in the Customer Success Manager position at Gusto. After reviewing your qualifications, we have decided to move forward with candidates whose qualifications better meet our current needs.\n\nSincerely,\nGusto Talent Acquisition", 15, "rejection"},
	{"Anduril Industries <noreply@icims.com>", "Anduril Industries Application Update",
		"Alex,\n\nThank you for your application for the Mechanical Engineer position. At this time we are unable to offer you a position. Your information will remain in our system.\n\nAnduril Industries Talent Acquisition", 13, "rejection"},
	{"Scale AI <no-reply@us.greenhouse-mail.io>", "Following up on your application",
		"Hi Alex,\n\nThanks so much for your interest in Scale AI and the Operations Associate role. Our team has reviewed your application and we won't be moving forward at this time. We truly appreciate the time you invested.\n\nBest,\nScale AI Recruiting", 14, "rejection"},
	{"Figma Recruiting <recruiting@figma.com>", "Figma Product Designer - next steps",
		"Hi Alex,\n\nThank you for meeting with the team last week. It was a difficult decision, but we have decided not to move forward with your candidacy for the Product Designer role. We really enjoyed getting to know you.\n\nTake care,\nJordan, Figma Recruiting", 4, "rejection"},
	{"Plaid <no-reply@hire.lever.co>", "Plaid - Software Engineer, Infrastructure",
		"Hi Alex,\n\nThank you for interviewing with Plaid. After much deliberation, the team has decided to go in a different direction for this role. We hope you will consider Plaid in the future.\n\nPlaid Recruiting", 8, "rejection"},
	{"Stripe Recruiting <no-reply@us.greenhouse-mail.io>", "Stripe: Data Engineer",
		"Hi Alex,\n\nThanks for applying for the Data Engineer position at Stripe. Unfortunately we're not able to move forward with your application. We encourage you to keep an eye on our careers page.\n\nStripe", 2, "rejection"},
	{"Airtable <no-reply@ashbyhq.com>", "Airtable Application",
		"Hi Alex,\n\nThank you for applying to the Solutions Engineer role at Airtable. Unfortunately, the role is no longer open, as we have filled it internally.\n\nAirtable Recruiting", 6, "rejection"},

	// Interview invitations and scheduling.
	{"Taylor Brooks <taylor.brooks@datadoghq.com>", "Datadog SRE - Phone screen",
		"Hi Alex,\n\nI'm a recruiter at Datadog and I came across your application for the Site Reliability Engineer role. I'd love to set up a 30 minute phone screen to learn more about you. What does your availability look like this week?\n\nBest,\nTaylor", 3, "interview"},
	{"Notion Recruiting <no-reply@ashbyhq.com>", "Schedule your interview with Notion",
		"Hi Alex,\n\nWe're excited to move you forward for the Software Engineer, New Grad role! Please use the link below to schedule your technical interview with our team.\n\nSchedule here: https://app.ashbyhq.com/scheduling/abc\n\nNotion Recruiting", 5, "interview"},
	{"Duolingo via Calendly <notifications@calendly.com>", "New Event: Alex Kim - 30 Minute Recruiter Call with Duolingo",
		"Hi Alex,\n\nA new event has been scheduled.\n\nEvent Type: 30 Minute Recruiter Call - Data Scientist\nInvitee: Alex Kim\nDate: Tuesday, December 2, 2026\n\nDuolingo Recruiting", 9, "interview"},
	{"Robinhood Recruiting <recruiting@robinhood.com>", "Invitation: Robinhood Security Engineer Interview",
		"Hi Alex,\n\nThank you for your interest in Robinhood! We'd like to invite you to interview for the Security Engineer position. The interview will be 45 minutes over Zoom with a member of our security team. Please let us know a few times that work for you.\n\nRobinhood Recruiting", 10, "interview"},
	{"Morgan Lee <morgan@stripe.com>", "Stripe Backend Engineer - next steps",
		"Hi Alex,\n\nThanks for applying to the Backend Engineer role. The hiring team reviewed your application and would like to move forward with a technical phone interview. Could you share your availability for next week?\n\nMorgan\nTechnical Recruiter, Stripe", 1, "interview"},
	{"Rippling <no-reply@us.greenhouse-mail.io>", "Rippling: Final round interview for Implementation Manager",
		"Hi Alex,\n\nCongratulations on moving to the final round! We'd like to schedule your onsite interviews for the Implementation Manager role. You will meet four members of the team over a half day. Please pick a time using the link below.\n\nRippling Recruiting", 12, "interview"},
	{"Brex <noreply@goodtime.io>", "Interview Confirmation - Brex",
		"Hi Alex,\n\nYour interview for the Staff Accountant role at Brex is confirmed for Thursday, December 4 at 2:00 PM CT. You will be speaking with Sam, our Accounting Manager, via Google Meet.\n\nBrex Recruiting", 11, "interview"},
	{"Gusto Talent <talent@gusto.com>", "Let's chat about the Customer Success Manager role",
		"Hi Alex,\n\nThanks for your application to Gusto! I'd love to schedule a quick call to chat about the Customer Success Manager role and your background. Are you available for a 20 minute conversation on Monday or Tuesday?\n\nCheers,\nRiley", 15, "interview"},
	{"Airtable <no-reply@ashbyhq.com>", "Airtable - Interview with the hiring manager",
		"Hi Alex,\n\nGreat news: the hiring manager for the Solutions Engineer role would like to meet you. Please select a time for a 45 minute video interview here: https://app.ashbyhq.com/s/xyz\n\nAirtable Recruiting", 6, "interview"},
	{"Plaid Recruiting <no-reply@hire.lever.co>", "Plaid onsite interview details",
		"Hi Alex,\n\nWe're looking forward to your virtual onsite for the Software Engineer, Infrastructure role. Below is your interview schedule for Friday: system design, coding, and a conversation with the hiring manager.\n\nPlaid Recruiting", 8, "interview"},
	{"Figma <recruiting@figma.com>", "Figma portfolio review",
		"Hi Alex,\n\nThe design team loved your work and would like to invite you to a portfolio review for the Product Designer role. Please send us a few times that work next week.\n\nFigma Recruiting", 4, "interview"},

	// Assessments.
	{"Notion <no-reply@hackerrank.com>", "Notion has invited you to a coding assessment",
		"Hi Alex,\n\nNotion has invited you to take a coding assessment for the Software Engineer, New Grad position. You have 7 days to complete the test. The assessment takes about 90 minutes.\n\nStart your test: https://www.hackerrank.com/test/abc", 5, "assessment"},
	{"Stripe Recruiting <no-reply@us.greenhouse-mail.io>", "Stripe Data Engineer: take-home exercise",
		"Hi Alex,\n\nThanks for your interest in the Data Engineer role. As the next step, we'd like you to complete a take-home exercise. Please submit your solution within five days.\n\nStripe Recruiting", 2, "assessment"},
	{"Scale AI <assessments@codesignal.com>", "Complete your Scale AI assessment",
		"Hello Alex,\n\nScale AI has invited you to complete a General Coding Assessment for the Operations Associate position. Please complete it within the next 5 days.\n\nCodeSignal", 14, "assessment"},
	{"Ramp <no-reply@ashbyhq.com>", "Ramp Financial Analyst - case study",
		"Hi Alex,\n\nThanks for applying to Ramp! The next step for the Financial Analyst role is a short case study. Please complete the attached modeling exercise and return it by Friday.\n\nRamp Talent", 7, "assessment"},

	// Offers.
	{"Rippling People Team <people@rippling.com>", "Your offer from Rippling",
		"Hi Alex,\n\nWe are thrilled to extend you an offer for the Implementation Manager position at Rippling! Your offer letter is attached; please review it and let us know if you have any questions.\n\nRippling People Team", 12, "offer"},
	{"Plaid <offers@plaid.com>", "Offer Letter - Plaid",
		"Hi Alex,\n\nCongratulations! We are pleased to offer you the position of Software Engineer, Infrastructure at Plaid. Please find your offer letter in DocuSign and sign by next Wednesday.\n\nPlaid Recruiting", 8, "offer"},
	{"Figma Recruiting <recruiting@figma.com>", "Figma - verbal offer follow up",
		"Hi Alex,\n\nIt was great speaking with you today! As discussed, we'd love to have you join Figma as a Product Designer. I'll send the formal offer letter over shortly with the compensation details we went over.\n\nJordan", 4, "offer"},

	// Application received.
	{"Ramp <no-reply@ashbyhq.com>", "Thanks for applying to Ramp",
		"Hi Alex,\n\nThanks for applying to the Financial Analyst role at Ramp! We have received your application and our team will review it shortly. If your experience is a match, we will reach out about next steps.\n\nRamp Talent", 7, "received"},
	{"Anduril Industries <noreply@icims.com>", "Thank you for applying to Anduril Industries",
		"Dear Alex,\n\nThank you for applying for the Mechanical Engineer position. We have received your application and are reviewing your qualifications. If there is a fit, a member of our team will contact you.\n\nAnduril Industries", 13, "received"},
	{"Notion <no-reply@ashbyhq.com>", "Application received: Software Engineer, New Grad",
		"Hi Alex,\n\nThis is a confirmation that we've received your application for Software Engineer, New Grad at Notion. Our team reviews every application carefully.\n\nNotion", 5, "received"},
	{"Gusto <gusto@myworkday.com>", "Gusto - We received your application",
		"Hello Alex,\n\nThank you for your interest in Gusto. Your application for Customer Success Manager has been successfully submitted. You can track the status of your application in your candidate home.\n\nGusto Talent Acquisition", 15, "received"},

	// Forwarded by hand: the original's sender is in the body.
	{"Alex Kim <alex.kim@example.com>", "Fwd: Your application to Datadog",
		"FYI\n\n---------- Forwarded message ---------\nFrom: Datadog <no-reply@hire.lever.co>\nDate: Mon, Nov 30, 2026 at 9:12 AM\nSubject: Your application to Datadog\nTo: <alex.kim@example.com>\n\nHi Alex,\n\nThank you for applying for the Site Reliability Engineer role. Unfortunately, we have decided to move forward with other candidates.\n\nDatadog Talent", 3, "rejection"},
	{"Alex Kim <alex.kim@example.com>", "FW: Brex interview",
		"-----Original Message-----\nFrom: Sam Ortiz <sam.ortiz@brex.com>\nSent: Tuesday, December 1, 2026 10:00 AM\nSubject: Brex interview\n\nHi Alex, we'd like to schedule a second round interview for the Staff Accountant role with our Controller. Do you have time on Thursday afternoon?\n\nSam", 11, "interview"},
	{"Alex Kim <alex.kim@example.com>", "Fwd: Offer",
		"Begin forwarded message:\n\nFrom: Duolingo Recruiting <recruiting@duolingo.com>\nSubject: Offer\nDate: December 3, 2026\n\nHi Alex,\n\nWe are excited to offer you the Data Scientist position at Duolingo. Your offer letter is attached.\n\nDuolingo", 9, "offer"},

	// Quoted-printable and HTML bodies come through Parse in TestCorpus.
	{"Airtable <no-reply@ashbyhq.com>", "Airtable: thanks for your application",
		"<html><body><p>Hi Alex,</p><p>Thanks for applying to the <b>Solutions Engineer</b> role at Airtable. We have received your application and will be in touch.</p></body></html>", 6, "received"},
	{"Robinhood <notifications@ashbyhq.com>", "Robinhood Security Engineer: technical interview",
		"<div>Hi Alex,</div><div>We'd like to invite you to a technical interview for the Security Engineer role. Please choose a time that works for you.</div>", 10, "interview"},

	// Several Stripe roles: the title decides which.
	{"Stripe Recruiting <no-reply@us.greenhouse-mail.io>", "Stripe interview invitation",
		"Hi Alex,\n\nThe Data Engineer hiring team would like to invite you to a video interview. Please use the link to schedule a time.\n\nStripe Recruiting", 2, "interview"},

	// A reminder of what is arranged says nothing new; a receipt for a job
	// already further along is read right and moves nothing (TestCorpus).
	{"Figma Recruiting <recruiting@figma.com>", "Figma interview reminder",
		"Hi Alex, a quick reminder of your interview tomorrow for the Product Designer role at 10am PT. See you then!", 4, ""},
	{"Plaid <no-reply@hire.lever.co>", "Thanks for applying to Plaid",
		"Hi Alex, thanks for applying to the Software Engineer, Infrastructure role. We have received your application.", 8, "received"},

	// Not about a tracked job.
	{"LinkedIn Job Alerts <jobalerts-noreply@linkedin.com>", "30+ new jobs for Software Engineer",
		"Software Engineer at Stripe - San Francisco\nBackend Engineer at Datadog - New York\nSee all jobs on LinkedIn.", 0, ""},
	{"Indeed <alert@indeed.com>", "Data Scientist jobs near you",
		"New jobs: Data Scientist - Duolingo - Pittsburgh. Apply now with your Indeed resume.", 0, ""},
	{"Jamie Chen <jamie@talentfirm.com>", "Exciting opportunity at a fintech startup",
		"Hi Alex, I'm a recruiter with TalentFirm and I'm working with a fast-growing fintech startup on a Senior Backend Engineer search. Would you be open to a quick call to discuss?", 0, ""},
	{"Coinbase <no-reply@us.greenhouse-mail.io>", "Your application to Coinbase",
		"Hi Alex, thank you for applying to Coinbase. Unfortunately, we have decided to move forward with other candidates.", 0, ""},
	{"Stripe <newsletter@stripe.com>", "Stripe Sessions 2027: early registration",
		"Join us at Stripe Sessions, our annual conference for the internet economy. Register now for early pricing.", 0, ""},
	{"Glassdoor <noreply@glassdoor.com>", "Companies you follow: Datadog has new reviews",
		"See what employees are saying about Datadog. 12 new reviews this week.", 0, ""},
	{"Handshake <no-reply@joinhandshake.com>", "Recommended interviews: on-campus recruiting",
		"Employers are scheduling interviews on Handshake this week. Browse open interview slots now.", 0, ""},
	{"Duolingo <hello@duolingo.com>", "You're on a 30 day streak!",
		"Keep it up! Practice Spanish for 5 minutes today to extend your streak.", 0, ""},
}
