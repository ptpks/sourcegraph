import * as H from 'history'
import React from 'react'
import renderer from 'react-test-renderer'
import { of } from 'rxjs'
import { SiteAdminOverviewPage } from './SiteAdminOverviewPage'
import { eventLogger } from '../tracking/eventLogger'
import sinon from 'sinon'
import { ISiteUsagePeriod } from '../../../shared/src/graphql/schema'

describe('SiteAdminOverviewPage', () => {
    const baseProps = {
        history: H.createMemoryHistory(),
        isLightTheme: true,
        overviewComponents: [],
    }

    let stub: sinon.SinonStub<[string, (boolean | undefined)?], void>

    beforeAll(() => {
        stub = sinon.stub(eventLogger, 'logViewEvent')
    })

    afterAll(() => {
        if (stub) {
            stub.restore()
        }
    })

    test('< 2 users', done => {
        const component = renderer.create(
            <SiteAdminOverviewPage
                {...baseProps}
                _fetchOverview={() =>
                    of({
                        repositories: 100,
                        users: 1,
                        orgs: 1,
                        surveyResponses: {
                            totalCount: 1,
                            averageScore: 10,
                        },
                    })
                }
                _fetchWeeklyActiveUsers={() =>
                    of({
                        __typename: 'SiteUsageStatistics',
                        daus: [],
                        waus: [],
                        maus: [],
                    })
                }
            />
        )
        // ensure the hooks ran and the "API response" has been received
        setTimeout(() => {
            expect(component.toJSON()).toMatchSnapshot()
            done()
        })
    })
    test('>= 2 users', done => {
        const usageStat: ISiteUsagePeriod = {
            __typename: 'SiteUsagePeriod',
            userCount: 10,
            registeredUserCount: 8,
            anonymousUserCount: 2,
            integrationUserCount: 0,
            startTime: new Date().toISOString(),
            stages: undefined as any,
        }
        const component = renderer.create(
            <SiteAdminOverviewPage
                {...baseProps}
                _fetchOverview={() =>
                    of({
                        repositories: 100,
                        users: 10,
                        orgs: 5,
                        surveyResponses: {
                            totalCount: 100,
                            averageScore: 10,
                        },
                    })
                }
                _fetchWeeklyActiveUsers={() =>
                    of({
                        __typename: 'SiteUsageStatistics',
                        daus: [],
                        waus: [usageStat, usageStat],
                        maus: [],
                    })
                }
            />
        )
        // ensure the hooks ran and the "API response" has been received
        setTimeout(() => {
            expect(component.toJSON()).toMatchSnapshot()
            done()
        })
    })
})
