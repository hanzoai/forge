{
  _config+:: {
    local c = self,
    dashboardNamePrefix: 'Hanzo Forge',
    dashboardTags: ['forge'],
    dashboardPeriod: 'now-1h',
    dashboardTimezone: 'default',
    dashboardRefresh: '1m',

    // please see https://docs.gitea.com/administration/config-cheat-sheet#metrics-metrics
    // Show issue by repository metrics with format git_issues_by_repository{repository="org/repo"} 5.
    // Requires ENABLED_ISSUE_BY_REPOSITORY set to true.
    showIssuesByRepository: true,
    // Show graphs for issue by label metrics with format git_issues_by_label{label="bug"} 2.
    // Requires ENABLED_ISSUE_BY_LABEL set to true.
    showIssuesByLabel: true,

    showIssuesOpenClose: true,

    // add or remove metrics from dashboard
    statMetrics:
      [
        {
          name: 'git_organizations',
          description: 'Organizations',
        },
        {
          name: 'git_teams',
          description: 'Teams',
        },
        {
          name: 'git_users',
          description: 'Users',
        },
        {
          name: 'git_repositories',
          description: 'Repositories',
        },
        {
          name: 'git_milestones',
          description: 'Milestones',
        },
        {
          name: 'git_stars',
          description: 'Stars',
        },
        {
          name: 'git_releases',
          description: 'Releases',
        },
      ]
      +
      if c.showIssuesOpenClose then
        [
          {
            name: 'git_issues_open',
            description: 'Issues opened',
          },
          {
            name: 'git_issues_closed',
            description: 'Issues closed',
          },
        ] else
        [
          {
            name: 'git_issues',
            description: 'Issues',
          },
        ],
    //set this for using label colors on graphs
    issueLabels: [
      {
        label: 'bug',
        color: '#ee0701',
      },
      {
        label: 'duplicate',
        color: '#cccccc',
      },
      {
        label: 'invalid',
        color: '#e6e6e6',
      },
      {
        label: 'enhancement',
        color: '#84b6eb',
      },
      {
        label: 'help wanted',
        color: '#128a0c',
      },
      {
        label: 'question',
        color: '#cc317c',
      },
    ],
  },
}
