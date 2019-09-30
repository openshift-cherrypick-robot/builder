package clusterresourcequota

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utildiff "k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1listers "k8s.io/client-go/listers/core/v1"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	coreapi "k8s.io/kubernetes/pkg/apis/core"

	quotaapiv1 "github.com/openshift/api/quota/v1"
	quotaapi "github.com/openshift/origin/pkg/quota/apis/quota"
	"github.com/openshift/origin/pkg/quota/controller/clusterquotamapping"
	fakequotaclient "github.com/openshift/origin/pkg/quota/generated/internalclientset/fake"
	quotalister "github.com/openshift/origin/pkg/quota/generated/listers/quota/internalversion"
)

func TestUpdateQuota(t *testing.T) {
	testCases := []struct {
		name            string
		availableQuotas func() []*quotaapi.ClusterResourceQuota
		quotaToUpdate   *coreapi.ResourceQuota

		expectedQuota func() *quotaapi.ClusterResourceQuota
		expectedError string
	}{
		{
			name: "update properly",
			availableQuotas: func() []*quotaapi.ClusterResourceQuota {
				user1 := defaultQuota()
				user1.Name = "user-one"
				user1.Status.Total.Hard = user1.Spec.Quota.Hard
				user1.Status.Total.Used = coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("15")}
				user1.Status.Namespaces.Insert("foo", coreapi.ResourceQuotaStatus{
					Hard: user1.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("5")},
				})
				user1.Status.Namespaces.Insert("bar", coreapi.ResourceQuotaStatus{
					Hard: user1.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("10")},
				})

				user2 := defaultQuota()
				user2.Name = "user-two"
				user2.Status.Total.Hard = user2.Spec.Quota.Hard
				user2.Status.Total.Used = coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("5")}
				user2.Status.Namespaces.Insert("foo", coreapi.ResourceQuotaStatus{
					Hard: user2.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("5")},
				})

				return []*quotaapi.ClusterResourceQuota{user1, user2}
			},
			quotaToUpdate: &coreapi.ResourceQuota{
				ObjectMeta: metav1.ObjectMeta{Namespace: "foo", Name: "user-one"},
				Spec: coreapi.ResourceQuotaSpec{
					Hard: coreapi.ResourceList{
						coreapi.ResourcePods:    resource.MustParse("10"),
						coreapi.ResourceSecrets: resource.MustParse("5"),
					},
				},
				Status: coreapi.ResourceQuotaStatus{
					Hard: coreapi.ResourceList{
						coreapi.ResourcePods:    resource.MustParse("10"),
						coreapi.ResourceSecrets: resource.MustParse("5"),
					},
					Used: coreapi.ResourceList{
						coreapi.ResourcePods: resource.MustParse("20"),
					},
				}},

			expectedQuota: func() *quotaapi.ClusterResourceQuota {
				user1 := defaultQuota()
				user1.Name = "user-one"
				user1.Status.Total.Hard = user1.Spec.Quota.Hard
				user1.Status.Total.Used = coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("20")}
				user1.Status.Namespaces.Insert("foo", coreapi.ResourceQuotaStatus{
					Hard: user1.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("10")},
				})
				user1.Status.Namespaces.Insert("bar", coreapi.ResourceQuotaStatus{
					Hard: user1.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("10")},
				})

				return user1
			},
		},
	}

	for _, tc := range testCases {
		quotaIndexer := cache.NewIndexer(cache.DeletionHandlingMetaNamespaceKeyFunc, cache.Indexers{})
		availableQuotas := tc.availableQuotas()
		objs := []runtime.Object{}
		for i := range availableQuotas {
			quotaIndexer.Add(availableQuotas[i])
			objs = append(objs, availableQuotas[i])
		}
		quotaLister := quotalister.NewClusterResourceQuotaLister(quotaIndexer)

		client := fakequotaclient.NewSimpleClientset(objs...)

		accessor := newQuotaAccessor(quotaLister, nil, client.Quota(), nil)

		actualErr := accessor.UpdateQuotaStatus(tc.quotaToUpdate)
		switch {
		case len(tc.expectedError) == 0 && actualErr == nil:
		case len(tc.expectedError) == 0 && actualErr != nil:
			t.Errorf("%s: unexpected error: %v", tc.name, actualErr)
			continue
		case len(tc.expectedError) != 0 && actualErr == nil:
			t.Errorf("%s: missing expected error: %v", tc.name, tc.expectedError)
			continue
		case len(tc.expectedError) != 0 && actualErr != nil && !strings.Contains(actualErr.Error(), tc.expectedError):
			t.Errorf("%s: expected %v, got %v", tc.name, tc.expectedError, actualErr)
			continue
		}

		var actualQuota *quotaapi.ClusterResourceQuota
		for _, action := range client.Actions() {
			updateAction, ok := action.(clientgotesting.UpdateActionImpl)
			if !ok {
				continue
			}
			if updateAction.Matches("update", "clusterresourcequotas") && updateAction.Subresource == "status" {
				actualQuota = updateAction.GetObject().(*quotaapi.ClusterResourceQuota)
				break
			}
		}

		expectedV1, err := legacyscheme.Scheme.ConvertToVersion(tc.expectedQuota(), quotaapiv1.SchemeGroupVersion)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		actualV1, err := legacyscheme.Scheme.ConvertToVersion(actualQuota, quotaapiv1.SchemeGroupVersion)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if !equality.Semantic.DeepEqual(expectedV1, actualV1) {
			t.Errorf("%s: %v", tc.name, utildiff.ObjectDiff(expectedV1, actualV1))
			continue
		}
	}

}

func defaultQuota() *quotaapi.ClusterResourceQuota {
	return &quotaapi.ClusterResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "foo"},
		Spec: quotaapi.ClusterResourceQuotaSpec{
			Quota: coreapi.ResourceQuotaSpec{
				Hard: coreapi.ResourceList{
					coreapi.ResourcePods:    resource.MustParse("10"),
					coreapi.ResourceSecrets: resource.MustParse("5"),
				},
			},
		},
	}
}

func TestGetQuota(t *testing.T) {
	testCases := []struct {
		name                string
		availableQuotas     func() []*quotaapi.ClusterResourceQuota
		availableNamespaces []*corev1.Namespace
		mapperFunc          func() clusterquotamapping.ClusterQuotaMapper
		requestedNamespace  string

		expectedQuotas func() []*coreapi.ResourceQuota
		expectedError  string
	}{
		{
			name: "namespace not synced",
			availableQuotas: func() []*quotaapi.ClusterResourceQuota {
				return nil
			},
			availableNamespaces: []*corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"one": "alfa"}}},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				mapper := newFakeClusterQuotaMapper()
				mapper.namespaceToQuota["foo"] = sets.NewString("user-one")
				mapper.namespaceToSelectionFields["foo"] = clusterquotamapping.SelectionFields{Labels: map[string]string{"two": "bravo"}}
				return mapper
			},
			requestedNamespace: "foo",

			expectedError: "timed out waiting for the condition",
		},
		{
			name: "no hits on namespace",
			availableQuotas: func() []*quotaapi.ClusterResourceQuota {
				return nil
			},
			availableNamespaces: []*corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"one": "alfa"}}},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				mapper := newFakeClusterQuotaMapper()
				mapper.namespaceToQuota["foo"] = sets.NewString()
				mapper.namespaceToSelectionFields["foo"] = clusterquotamapping.SelectionFields{Labels: map[string]string{"one": "alfa"}}
				mapper.namespaceToQuota["bar"] = sets.NewString("user-one")
				mapper.namespaceToSelectionFields["bar"] = clusterquotamapping.SelectionFields{Labels: map[string]string{"two": "bravo"}}
				return mapper
			},
			requestedNamespace: "foo",

			expectedQuotas: func() []*coreapi.ResourceQuota {
				return []*coreapi.ResourceQuota{}
			},
			expectedError: "",
		},
		{
			name: "correct quota and namespaces",
			availableQuotas: func() []*quotaapi.ClusterResourceQuota {
				user1 := defaultQuota()
				user1.Name = "user-one"
				user1.Status.Total.Hard = user1.Spec.Quota.Hard
				user1.Status.Total.Used = coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("15")}
				user1.Status.Namespaces.Insert("foo", coreapi.ResourceQuotaStatus{
					Hard: user1.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("5")},
				})
				user1.Status.Namespaces.Insert("bar", coreapi.ResourceQuotaStatus{
					Hard: user1.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("10")},
				})

				user2 := defaultQuota()
				user2.Name = "user-two"
				user2.Status.Total.Hard = user2.Spec.Quota.Hard
				user2.Status.Total.Used = coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("5")}
				user2.Status.Namespaces.Insert("foo", coreapi.ResourceQuotaStatus{
					Hard: user2.Spec.Quota.Hard,
					Used: coreapi.ResourceList{coreapi.ResourcePods: resource.MustParse("5")},
				})

				return []*quotaapi.ClusterResourceQuota{user1, user2}
			},
			availableNamespaces: []*corev1.Namespace{
				{ObjectMeta: metav1.ObjectMeta{Name: "foo", Labels: map[string]string{"one": "alfa"}}},
			},
			mapperFunc: func() clusterquotamapping.ClusterQuotaMapper {
				mapper := newFakeClusterQuotaMapper()
				mapper.namespaceToQuota["foo"] = sets.NewString("user-one")
				mapper.namespaceToSelectionFields["foo"] = clusterquotamapping.SelectionFields{Labels: map[string]string{"one": "alfa"}}
				return mapper
			},
			requestedNamespace: "foo",

			expectedQuotas: func() []*coreapi.ResourceQuota {
				return []*coreapi.ResourceQuota{
					{
						ObjectMeta: metav1.ObjectMeta{Namespace: "foo", Name: "user-one"},
						Spec: coreapi.ResourceQuotaSpec{
							Hard: coreapi.ResourceList{
								coreapi.ResourcePods:    resource.MustParse("10"),
								coreapi.ResourceSecrets: resource.MustParse("5"),
							},
						},
						Status: coreapi.ResourceQuotaStatus{
							Hard: coreapi.ResourceList{
								coreapi.ResourcePods:    resource.MustParse("10"),
								coreapi.ResourceSecrets: resource.MustParse("5"),
							},
							Used: coreapi.ResourceList{
								coreapi.ResourcePods: resource.MustParse("15"),
							},
						},
					},
				}
			},
		},
	}

	for _, tc := range testCases {
		quotaIndexer := cache.NewIndexer(cache.DeletionHandlingMetaNamespaceKeyFunc, cache.Indexers{})
		availableQuotas := tc.availableQuotas()
		for i := range availableQuotas {
			quotaIndexer.Add(availableQuotas[i])
		}
		quotaLister := quotalister.NewClusterResourceQuotaLister(quotaIndexer)

		namespaceIndexer := cache.NewIndexer(cache.DeletionHandlingMetaNamespaceKeyFunc, cache.Indexers{})
		for i := range tc.availableNamespaces {
			namespaceIndexer.Add(tc.availableNamespaces[i])
		}
		namespaceLister := corev1listers.NewNamespaceLister(namespaceIndexer)

		client := fakequotaclient.NewSimpleClientset()

		accessor := newQuotaAccessor(quotaLister, namespaceLister, client.Quota(), tc.mapperFunc())

		actualQuotas, actualErr := accessor.GetQuotas(tc.requestedNamespace)
		switch {
		case len(tc.expectedError) == 0 && actualErr == nil:
		case len(tc.expectedError) == 0 && actualErr != nil:
			t.Errorf("%s: unexpected error: %v", tc.name, actualErr)
			continue
		case len(tc.expectedError) != 0 && actualErr == nil:
			t.Errorf("%s: missing expected error: %v", tc.name, tc.expectedError)
			continue
		case len(tc.expectedError) != 0 && actualErr != nil && !strings.Contains(actualErr.Error(), tc.expectedError):
			t.Errorf("%s: expected %v, got %v", tc.name, tc.expectedError, actualErr)
			continue
		}

		if tc.expectedQuotas == nil {
			continue
		}

		actualQuotaPointers := []*coreapi.ResourceQuota{}
		for i := range actualQuotas {
			actualQuotaPointers = append(actualQuotaPointers, &actualQuotas[i])
		}

		expectedQuotas := tc.expectedQuotas()
		if !equality.Semantic.DeepEqual(expectedQuotas, actualQuotaPointers) {
			t.Errorf("%s: expectedLen: %v actualLen: %v", tc.name, len(expectedQuotas), len(actualQuotas))
			for i := range expectedQuotas {
				expectedV1, err := legacyscheme.Scheme.ConvertToVersion(expectedQuotas[i], quotaapiv1.SchemeGroupVersion)
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tc.name, err)
					continue
				}
				actualV1, err := legacyscheme.Scheme.ConvertToVersion(actualQuotaPointers[i], quotaapiv1.SchemeGroupVersion)
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tc.name, err)
					continue
				}
				t.Errorf("%s: %v equal? %v", tc.name, utildiff.ObjectDiff(expectedV1, actualV1), equality.Semantic.DeepEqual(expectedV1, actualV1))
			}
			continue
		}
	}
}

type fakeClusterQuotaMapper struct {
	quotaToSelector            map[string]quotaapi.ClusterResourceQuotaSelector
	namespaceToSelectionFields map[string]clusterquotamapping.SelectionFields

	quotaToNamespaces map[string]sets.String
	namespaceToQuota  map[string]sets.String
}

func newFakeClusterQuotaMapper() *fakeClusterQuotaMapper {
	return &fakeClusterQuotaMapper{
		quotaToSelector:            map[string]quotaapi.ClusterResourceQuotaSelector{},
		namespaceToSelectionFields: map[string]clusterquotamapping.SelectionFields{},
		quotaToNamespaces:          map[string]sets.String{},
		namespaceToQuota:           map[string]sets.String{},
	}
}

func (m *fakeClusterQuotaMapper) GetClusterQuotasFor(namespaceName string) ([]string, clusterquotamapping.SelectionFields) {
	return m.namespaceToQuota[namespaceName].List(), m.namespaceToSelectionFields[namespaceName]
}
func (m *fakeClusterQuotaMapper) GetNamespacesFor(quotaName string) ([]string, quotaapi.ClusterResourceQuotaSelector) {
	return m.quotaToNamespaces[quotaName].List(), m.quotaToSelector[quotaName]
}
func (m *fakeClusterQuotaMapper) AddListener(listener clusterquotamapping.MappingChangeListener) {}