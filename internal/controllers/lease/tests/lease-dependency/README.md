# Create a Lease with a flavor reservation and its dependency

## Step 00

Create a Lease reserving instances of a flavor which does not exist yet, and verify that it is waiting for the Flavor.

## Step 01

Import the DevStack flavor as the referenced Flavor, and verify that the Lease is created with a flavor reservation.

## Step 02

Delete the Flavor, and verify that the Lease is not affected: Blazar copies the size of the flavor when the lease is created.

## Reference

https://k-orc.cloud/development/writing-tests/#dependency
