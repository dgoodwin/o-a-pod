oc new-project ansible-test
kubectl create secret generic ssh-private-key --from-file=ssh-privatekey=/home/dgoodwin/libra.pem
oc create serviceaccount openshift-ansible -n ansible-test
go build
./o-a-pod /path/to/ansible/inventory
